package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

var _ scribev1connect.WorkspaceServiceHandler = (*Manager)(nil)

func (m *Manager) ListWorkspaces(ctx context.Context, _ *connect.Request[scribev1.ListWorkspacesRequest]) (*connect.Response[scribev1.ListWorkspacesResponse], error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}

	workspaces, err := m.identities.ListWorkspaceAccessByUser(ctx, principal.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &scribev1.ListWorkspacesResponse{
		Workspaces: make([]*scribev1.WorkspaceAccess, 0, len(workspaces)),
	}
	for _, access := range workspaces {
		resp.Workspaces = append(resp.Workspaces, workspaceAccessToProto(access))
	}
	return connect.NewResponse(resp), nil
}

func (m *Manager) CreateWorkspace(ctx context.Context, req *connect.Request[scribev1.CreateWorkspaceRequest]) (*connect.Response[scribev1.CreateWorkspaceResponse], error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}

	workspace, err := m.identities.CreateWorkspaceForUser(ctx, principal.UserID, req.Msg.GetName())
	if err != nil {
		if errors.Is(err, store.ErrWorkspaceAccessLimit) {
			return nil, connect.NewError(connect.CodeResourceExhausted, err)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create workspace persistence failed"))
	}
	return connect.NewResponse(&scribev1.CreateWorkspaceResponse{
		Workspace: workspaceAccessToProto(workspace),
	}), nil
}

func (m *Manager) UpdateWorkspace(ctx context.Context, req *connect.Request[scribev1.UpdateWorkspaceRequest]) (*connect.Response[scribev1.UpdateWorkspaceResponse], error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	workspace, err := m.identities.UpdateWorkspaceName(ctx, req.Msg.GetWorkspaceId(), req.Msg.GetName())
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		case errors.Is(err, store.ErrPersonalWorkspaceImmutable):
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update workspace persistence failed"))
		}
	}
	return connect.NewResponse(&scribev1.UpdateWorkspaceResponse{
		Workspace: workspaceToProto(workspace),
	}), nil
}

func (m *Manager) ListWorkspaceMembers(ctx context.Context, req *connect.Request[scribev1.ListWorkspaceMembersRequest]) (*connect.Response[scribev1.ListWorkspaceMembersResponse], error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	workspace, err := m.identities.GetWorkspace(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	members, err := m.identities.ListWorkspaceMembers(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &scribev1.ListWorkspaceMembersResponse{
		Workspace: workspaceToProto(workspace),
		Members:   make([]*scribev1.WorkspaceMember, 0, len(members)),
	}
	for _, member := range members {
		resp.Members = append(resp.Members, workspaceMemberToProto(member))
	}
	return connect.NewResponse(resp), nil
}

func (m *Manager) AddWorkspaceMember(ctx context.Context, req *connect.Request[scribev1.AddWorkspaceMemberRequest]) (*connect.Response[scribev1.AddWorkspaceMemberResponse], error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	member, err := m.identities.AddWorkspaceMemberByEmail(ctx, req.Msg.GetWorkspaceId(), req.Msg.GetEmail(), req.Msg.GetRole())
	if err != nil {
		return nil, workspaceMutationError(err)
	}
	return connect.NewResponse(&scribev1.AddWorkspaceMemberResponse{
		Member: workspaceMemberToProto(member),
	}), nil
}

func (m *Manager) UpdateWorkspaceMember(ctx context.Context, req *connect.Request[scribev1.UpdateWorkspaceMemberRequest]) (*connect.Response[scribev1.UpdateWorkspaceMemberResponse], error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	member, err := m.identities.UpdateWorkspaceMemberRole(ctx, req.Msg.GetWorkspaceId(), req.Msg.GetUserId(), req.Msg.GetRole())
	if err != nil {
		return nil, workspaceMutationError(err)
	}
	return connect.NewResponse(&scribev1.UpdateWorkspaceMemberResponse{
		Member: workspaceMemberToProto(member),
	}), nil
}

func (m *Manager) DeleteWorkspaceMember(ctx context.Context, req *connect.Request[scribev1.DeleteWorkspaceMemberRequest]) (*connect.Response[scribev1.DeleteWorkspaceMemberResponse], error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	err := m.identities.RemoveWorkspaceMember(ctx, req.Msg.GetWorkspaceId(), req.Msg.GetUserId())
	if err != nil {
		return nil, workspaceMutationError(err)
	}
	return connect.NewResponse(&scribev1.DeleteWorkspaceMemberResponse{}), nil
}

func (m *Manager) sessionPrincipal(ctx context.Context) (Principal, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok || principal.Anonymous() {
		return Principal{}, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if !strings.EqualFold(principal.AuthType, "session") {
		return Principal{}, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("session authentication required"))
	}
	return principal, nil
}

func workspaceMutationError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace member not found"))
	case errors.Is(err, store.ErrWorkspaceUserNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, store.ErrWorkspaceMemberExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, store.ErrInvalidWorkspaceRole):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, store.ErrLastWorkspaceAdmin):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, store.ErrPersonalWorkspaceImmutable):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, store.ErrWorkspaceAccessLimit), errors.Is(err, store.ErrWorkspaceMemberLimit):
		return connect.NewError(connect.CodeResourceExhausted, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func workspaceToProto(workspace store.Workspace) *scribev1.Workspace {
	protoWorkspace := &scribev1.Workspace{
		Id:         workspace.ID,
		Name:       workspace.Name,
		Slug:       workspace.Slug,
		IsPersonal: workspace.IsPersonal,
		CreatedAt:  workspace.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  workspace.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if workspace.OwnerUserID != nil {
		protoWorkspace.OwnerUserId = *workspace.OwnerUserID
	}
	return protoWorkspace
}

func workspaceAccessToProto(access store.WorkspaceAccess) *scribev1.WorkspaceAccess {
	return &scribev1.WorkspaceAccess{
		Workspace: workspaceToProto(access.Workspace),
		Role:      access.Role,
	}
}

func workspaceMemberToProto(member store.WorkspaceMember) *scribev1.WorkspaceMember {
	return &scribev1.WorkspaceMember{
		WorkspaceId: member.WorkspaceID,
		Role:        member.Role,
		CreatedAt:   member.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		User: &scribev1.UserProfile{
			Id:         member.User.ID,
			Name:       member.User.Name,
			Email:      member.User.Email,
			PictureUrl: member.User.PictureURL,
			IsAdmin:    member.User.IsAdmin,
		},
	}
}
