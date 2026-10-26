package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

var _ scribev1connect.WorkspaceServiceHandler = (*Manager)(nil)

func (m *Manager) ListWorkspaces(ctx context.Context, _ *scribev1.ListWorkspacesRequest) (*scribev1.ListWorkspacesResponse, error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}

	workspaces, err := m.identities.ListWorkspaceAccessByUser(ctx, principal.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}

	resp := &scribev1.ListWorkspacesResponse{
		Workspaces: make([]*scribev1.WorkspaceAccess, 0, len(workspaces)),
	}
	for _, access := range workspaces {
		resp.Workspaces = append(resp.Workspaces, workspaceAccessToProto(access))
	}
	return resp, nil
}

func (m *Manager) CreateWorkspace(ctx context.Context, req *scribev1.CreateWorkspaceRequest) (*scribev1.CreateWorkspaceResponse, error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}

	workspace, err := m.identities.CreateWorkspaceForUser(ctx, principal.UserID, req.GetName())
	if err != nil {
		if errors.Is(err, store.ErrWorkspaceAccessLimit) {
			return nil, connect.NewError(connect.CodeResourceExhausted, err.Error()).WithCause(err)
		}
		return nil, connect.Errorf(connect.CodeInternal, "create workspace persistence failed")
	}
	return &scribev1.CreateWorkspaceResponse{
		Workspace: workspaceAccessToProto(workspace),
	}, nil
}

func (m *Manager) UpdateWorkspace(ctx context.Context, req *scribev1.UpdateWorkspaceRequest) (*scribev1.UpdateWorkspaceResponse, error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	workspace, err := m.identities.UpdateWorkspaceName(ctx, req.GetWorkspaceId(), req.GetName())
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, connect.Errorf(connect.CodeNotFound, "workspace not found")
		default:
			return nil, connect.Errorf(connect.CodeInternal, "update workspace persistence failed")
		}
	}
	return &scribev1.UpdateWorkspaceResponse{
		Workspace: workspaceToProto(workspace),
	}, nil
}

func (m *Manager) ListWorkspaceMembers(ctx context.Context, req *scribev1.ListWorkspaceMembersRequest) (*scribev1.ListWorkspaceMembersResponse, error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	workspace, err := m.identities.GetWorkspace(ctx, req.GetWorkspaceId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.Errorf(connect.CodeNotFound, "workspace not found")
		}
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	members, err := m.identities.ListWorkspaceMembers(ctx, req.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}

	resp := &scribev1.ListWorkspaceMembersResponse{
		Workspace: workspaceToProto(workspace),
		Members:   make([]*scribev1.WorkspaceMember, 0, len(members)),
	}
	for _, member := range members {
		resp.Members = append(resp.Members, workspaceMemberToProto(member))
	}
	return resp, nil
}

func (m *Manager) AddWorkspaceMember(ctx context.Context, req *scribev1.AddWorkspaceMemberRequest) (*scribev1.AddWorkspaceMemberResponse, error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	member, err := m.identities.AddWorkspaceMemberByEmail(ctx, req.GetWorkspaceId(), req.GetEmail(), req.GetRole())
	if err != nil {
		return nil, workspaceMutationError(err)
	}
	return &scribev1.AddWorkspaceMemberResponse{
		Member: workspaceMemberToProto(member),
	}, nil
}

func (m *Manager) UpdateWorkspaceMember(ctx context.Context, req *scribev1.UpdateWorkspaceMemberRequest) (*scribev1.UpdateWorkspaceMemberResponse, error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	member, err := m.identities.UpdateWorkspaceMemberRole(ctx, req.GetWorkspaceId(), req.GetUserId(), req.GetRole())
	if err != nil {
		return nil, workspaceMutationError(err)
	}
	return &scribev1.UpdateWorkspaceMemberResponse{
		Member: workspaceMemberToProto(member),
	}, nil
}

func (m *Manager) DeleteWorkspaceMember(ctx context.Context, req *scribev1.DeleteWorkspaceMemberRequest) (*scribev1.DeleteWorkspaceMemberResponse, error) {
	if _, err := m.sessionPrincipal(ctx); err != nil {
		return nil, err
	}

	err := m.identities.RemoveWorkspaceMember(ctx, req.GetWorkspaceId(), req.GetUserId())
	if err != nil {
		return nil, workspaceMutationError(err)
	}
	return &scribev1.DeleteWorkspaceMemberResponse{}, nil
}

func (m *Manager) sessionPrincipal(ctx context.Context) (Principal, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok || principal.Anonymous() {
		return Principal{}, connect.Errorf(connect.CodeUnauthenticated, "authentication required")
	}
	if !strings.EqualFold(principal.AuthType, "session") {
		return Principal{}, connect.Errorf(connect.CodePermissionDenied, "session authentication required")
	}
	return principal, nil
}

func workspaceMutationError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return connect.Errorf(connect.CodeNotFound, "workspace member not found")
	case errors.Is(err, store.ErrWorkspaceUserNotFound):
		return connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	case errors.Is(err, store.ErrWorkspaceMemberExists):
		return connect.NewError(connect.CodeAlreadyExists, err.Error()).WithCause(err)
	case errors.Is(err, store.ErrInvalidWorkspaceRole):
		return connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	case errors.Is(err, store.ErrLastWorkspaceAdmin):
		return connect.NewError(connect.CodeFailedPrecondition, err.Error()).WithCause(err)
	case errors.Is(err, store.ErrWorkspaceAccessLimit), errors.Is(err, store.ErrWorkspaceMemberLimit):
		return connect.NewError(connect.CodeResourceExhausted, err.Error()).WithCause(err)
	default:
		return connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
}

func workspaceToProto(workspace store.Workspace) *scribev1.Workspace {
	protoWorkspace := &scribev1.Workspace{
		Id:        workspace.ID,
		Name:      workspace.Name,
		Slug:      workspace.Slug,
		CreatedAt: workspace.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: workspace.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
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
