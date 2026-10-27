package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

var _ scribev1connect.WebhookServiceHandler = (*Handler)(nil)

func (h *Handler) SetWebhookSubscriptionStore(subscriptions *store.WebhookSubscriptionStore) {
	if h != nil {
		h.webhookSubscriptions = subscriptions
	}
}

func (h *Handler) CreateWebhook(ctx context.Context, req *scribev1.CreateWebhookRequest) (*scribev1.CreateWebhookResponse, error) {
	if h.webhookSubscriptions == nil {
		return nil, connect.Errorf(connect.CodeInternal, "webhook subscription repository is not configured")
	}
	targetURL, err := normalizeWebhookTargetURL(req.GetTargetUrl())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	subscription, err := h.webhookSubscriptions.Create(ctx, req.GetWorkspaceId(), targetURL, req.GetSecret())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrWebhookSubscriptionExists):
			return nil, connect.NewError(connect.CodeAlreadyExists, err.Error()).WithCause(err)
		case errors.Is(err, store.ErrWebhookSubscriptionLimit):
			return nil, connect.NewError(connect.CodeResourceExhausted, err.Error()).WithCause(err)
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create webhook subscription: %w", err).Error()).WithCause(err)
		}
	}
	return &scribev1.CreateWebhookResponse{Webhook: webhookSubscriptionToProto(subscription)}, nil
}

func (h *Handler) ListWebhooks(ctx context.Context, req *scribev1.ListWebhooksRequest) (*scribev1.ListWebhooksResponse, error) {
	if h.webhookSubscriptions == nil {
		return nil, connect.Errorf(connect.CodeInternal, "webhook subscription repository is not configured")
	}
	subscriptions, err := h.webhookSubscriptions.List(ctx, req.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list webhook subscriptions: %w", err).Error()).WithCause(err)
	}
	response := &scribev1.ListWebhooksResponse{Webhooks: make([]*scribev1.WebhookSubscription, 0, len(subscriptions))}
	for _, subscription := range subscriptions {
		response.Webhooks = append(response.Webhooks, webhookSubscriptionToProto(subscription))
	}
	return response, nil
}

func (h *Handler) DeleteWebhook(ctx context.Context, req *scribev1.DeleteWebhookRequest) (*scribev1.DeleteWebhookResponse, error) {
	if h.webhookSubscriptions == nil {
		return nil, connect.Errorf(connect.CodeInternal, "webhook subscription repository is not configured")
	}
	if err := h.webhookSubscriptions.Delete(ctx, req.GetWorkspaceId(), req.GetWebhookId()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.Errorf(connect.CodeNotFound, "webhook subscription not found")
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete webhook subscription: %w", err).Error()).WithCause(err)
	}
	return &scribev1.DeleteWebhookResponse{}, nil
}

func webhookSubscriptionToProto(subscription store.WebhookSubscription) *scribev1.WebhookSubscription {
	return &scribev1.WebhookSubscription{
		Id:          subscription.ID,
		WorkspaceId: subscription.WorkspaceID,
		TargetUrl:   subscription.TargetURL,
		CreatedAt:   subscription.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   subscription.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func normalizeWebhookTargetURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("target_url must be an absolute public HTTPS URL without credentials, query, or fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return "", fmt.Errorf("target_url must use a public hostname")
	}
	if err := safehttp.ValidatePublicURL(parsed); err != nil {
		return "", fmt.Errorf("target_url must use a public HTTPS address")
	}
	parsed.Scheme = "https"
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}
