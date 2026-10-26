package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

func TestNormalizeWebhookTargetURLRequiresPublicHTTPSOrigin(t *testing.T) {
	t.Parallel()

	got, err := normalizeWebhookTargetURL(" HTTPS://Hooks.Example.COM/scribe/events ")
	if err != nil {
		t.Fatalf("normalize public webhook URL: %v", err)
	}
	if got != "https://hooks.example.com/scribe/events" {
		t.Fatalf("normalized webhook URL = %q", got)
	}

	for name, raw := range map[string]string{
		"empty":        "",
		"http":         "http://hooks.example.com/events",
		"credentials":  "https://user:secret@hooks.example.com/events",
		"query":        "https://hooks.example.com/events?token=secret",
		"fragment":     "https://hooks.example.com/events#secret",
		"localhost":    "https://localhost/events",
		"local suffix": "https://hooks.internal/events",
		"ipv4 private": "https://127.0.0.1/events",
		"ipv6 private": "https://[::1]/events",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if normalized, err := normalizeWebhookTargetURL(raw); err == nil {
				t.Fatalf("normalizeWebhookTargetURL(%q) = %q, want rejection", raw, normalized)
			}
		})
	}
}

func TestWebhookServiceCreatesListsAndDeletesWorkspaceSubscription(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	workspaceA, userA := createServerTestWorkspace(t, database)
	workspaceB, userB := createServerTestWorkspace(t, database)
	handler := NewHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetWebhookSubscriptionStore(store.NewWebhookSubscriptionStore(database))
	appServer := newTenantScopedServer(t, handler, map[string]testTenantIdentity{
		"a": {workspaceID: workspaceA, userID: userA},
		"b": {workspaceID: workspaceB, userID: userB},
	})
	client := scribev1connect.NewWebhookServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, appServer.URL, connecthttp.WithReadMaxBytes(0))))
	targetURL := "https://hooks.example.com/" + uuid.NewString()
	request := &scribev1.CreateWebhookRequest{
		WorkspaceId: workspaceA,
		TargetUrl:   targetURL,
		Secret:      strings.Repeat("s", store.MinWebhookSigningSecretBytes),
	}
	created, err := client.CreateWebhook(tenantConnectContext(ctx, "a"), request)
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	if created.GetWebhook().GetId() == 0 || created.GetWebhook().GetWorkspaceId() != workspaceA || created.GetWebhook().GetTargetUrl() != targetURL {
		t.Fatalf("created webhook = %+v", created.GetWebhook())
	}
	t.Cleanup(func() {
		_ = store.NewWebhookSubscriptionStore(database).Delete(context.Background(), workspaceA, created.GetWebhook().GetId())
	})
	if _, err := client.CreateWebhook(tenantConnectContext(ctx, "a"), request); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate CreateWebhook error = %v/%v, want already_exists", connect.CodeOf(err), err)
	}
	listed, err := client.ListWebhooks(tenantConnectContext(ctx, "a"), &scribev1.ListWebhooksRequest{WorkspaceId: workspaceA})
	if err != nil || len(listed.GetWebhooks()) != 1 || listed.GetWebhooks()[0].GetId() != created.GetWebhook().GetId() {
		t.Fatalf("ListWebhooks workspace A = %+v/%v", listed, err)
	}
	listedB, err := client.ListWebhooks(tenantConnectContext(ctx, "b"), &scribev1.ListWebhooksRequest{WorkspaceId: workspaceB})
	if err != nil || len(listedB.GetWebhooks()) != 0 {
		t.Fatalf("ListWebhooks workspace B = %+v/%v", listedB, err)
	}
	if _, err := client.DeleteWebhook(tenantConnectContext(ctx, "b"), &scribev1.DeleteWebhookRequest{
		WorkspaceId: workspaceB, WebhookId: created.GetWebhook().GetId(),
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("cross-workspace DeleteWebhook error = %v/%v, want not_found", connect.CodeOf(err), err)
	}
	if _, err := client.DeleteWebhook(tenantConnectContext(ctx, "a"), &scribev1.DeleteWebhookRequest{
		WorkspaceId: workspaceA, WebhookId: created.GetWebhook().GetId(),
	}); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	listed, err = client.ListWebhooks(tenantConnectContext(ctx, "a"), &scribev1.ListWebhooksRequest{WorkspaceId: workspaceA})
	if err != nil || len(listed.GetWebhooks()) != 0 {
		t.Fatalf("ListWebhooks after delete = %+v/%v", listed, err)
	}
}
