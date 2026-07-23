package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

type registeredProviderTransportFunc func(*http.Request) (*http.Response, error)

func (f registeredProviderTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestProviderRedactionAcrossRegisteredAdapterLogsAndListedAudit(t *testing.T) {
	const (
		providerCredential = "sentinel-private-provider-credential"
		responseContent    = "sentinel-private-provider-response"
		untrustedModel     = "sentinel-provider-controlled-effective-model"
		model              = "gpt-4o"
	)

	database := openTestDB(t)
	ctx := context.Background()
	itemStore := store.NewItemStore(database)
	itemID := "provider-redaction-" + uuid.NewString()
	if _, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          itemID,
		UserID:      store.AnonymousUserID,
		WorkspaceID: store.AnonymousWorkspaceID,
		Name:        "provider redaction acceptance",
		SourceType:  "upload",
	}); err != nil {
		t.Fatalf("create provider redaction item: %v", err)
	}
	t.Cleanup(func() {
		_ = itemStore.DeleteForWorkspace(context.Background(), itemID, store.AnonymousWorkspaceID)
	})
	image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID:    itemID,
		Sequence:  1,
		ImageURL:  "https://source.example/image/" + uuid.NewString() + ".png",
		CanvasURI: "https://source.example/canvas/" + uuid.NewString(),
		Width:     1,
		Height:    1,
	})
	if err != nil {
		t.Fatalf("create provider redaction image: %v", err)
	}

	previousRuntime := config.Get()
	runtime := previousRuntime
	runtime.Config.LLM.OpenAI.Model = model
	runtime.Config.LLM.OpenAI.Models = []string{model}
	runtime.Secrets.OpenAIAPIKey = providerCredential
	config.Init(runtime)
	t.Cleanup(func() { config.Init(previousRuntime) })

	adversarialBody := fmt.Sprintf(
		`{"error":"%s","reflected_credential":"%s"}`,
		responseContent,
		providerCredential,
	)
	authorization := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(adversarialBody))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse provider test URL: %v", err)
	}
	targetURL := upstreamURL

	registeredRequest := make(chan string, 1)
	testTransport := upstream.Client().Transport
	providerHTTPClient := &http.Client{Transport: registeredProviderTransportFunc(func(request *http.Request) (*http.Response, error) {
		registeredRequest <- request.Method + " " + request.URL.String()
		authorization <- request.Header.Get("Authorization")
		rewritten := request.Clone(request.Context())
		rewrittenURL := *request.URL
		rewrittenURL.Scheme = targetURL.Scheme
		rewrittenURL.Host = targetURL.Host
		rewritten.URL = &rewrittenURL
		return testTransport.RoundTrip(rewritten)
	})}
	receive := func(channel <-chan string, label string) string {
		t.Helper()
		select {
		case value := <-channel:
			return value
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", label)
			return ""
		}
	}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	audits := store.NewProviderCallAuditStore(database)
	service := hocr.NewService(providerregistry.WithVendorHTTPClient(providerHTTPClient))
	service.SetProviderCallAuditLogger(providerCallAuditLogger(audits))
	imagePath := filepath.Join(t.TempDir(), "provider-input.png")
	if err := os.WriteFile(imagePath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o600); err != nil {
		t.Fatalf("write provider input: %v", err)
	}
	callCtx := hocr.WithProviderCallMetadata(
		ctx,
		store.AnonymousWorkspaceID,
		"provider-redaction-"+uuid.NewString(),
		&image.ID,
		nil,
	)
	_, providerErr := service.TranscribeImageWithContext(callCtx, imagePath, "openai", model)
	if providerErr == nil {
		t.Fatal("TranscribeImageWithContext() error = nil")
	}
	const safeFailure = "provider request failed with HTTP status 503"
	if got, want := providerErr.Error(), "failed to transcribe image: "+safeFailure; got != want {
		t.Fatalf("returned provider error = %q, want %q", got, want)
	}
	if got := receive(registeredRequest, "registered provider request"); got != "POST https://api.openai.com/v1/chat/completions" {
		t.Fatalf("registered provider request = %q", got)
	}
	if got := receive(authorization, "provider authorization"); got != "Bearer "+providerCredential {
		t.Fatalf("provider Authorization header did not use the configured credential")
	}

	listHandler := &Handler{items: itemStore, providerCallAudits: audits}
	listed, err := listHandler.ListItemProviderCallAudits(
		ctx,
		connect.NewRequest(&scribev1.ListItemProviderCallAuditsRequest{ItemId: itemID, Limit: 10}),
	)
	if err != nil {
		t.Fatalf("ListItemProviderCallAudits() error = %v", err)
	}
	if len(listed.Msg.GetAudits()) != 1 {
		t.Fatalf("listed provider audits = %d, want 1", len(listed.Msg.GetAudits()))
	}
	audit := listed.Msg.GetAudits()[0]
	if audit.GetProvider() != "openai" ||
		audit.GetModel() != model ||
		audit.GetOperation() != "transcribe_image" ||
		audit.GetErrorMessage() != safeFailure ||
		audit.GetHttpStatus() != http.StatusServiceUnavailable {
		t.Fatalf("listed provider audit retained insufficient diagnostics: %+v", audit)
	}

	successUpstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"model": untrustedModel,
			"choices": []any{map[string]any{
				"message": map[string]any{"content": "safe transcription"},
			}},
		})
	}))
	defer successUpstream.Close()
	successURL, err := url.Parse(successUpstream.URL)
	if err != nil {
		t.Fatalf("parse successful provider test URL: %v", err)
	}
	targetURL = successURL
	testTransport = successUpstream.Client().Transport
	text, successErr := service.TranscribeImageWithContext(callCtx, imagePath, "openai", model)
	if successErr != nil {
		t.Fatalf("successful registered provider call: %v", successErr)
	}
	if text != "safe transcription" {
		t.Fatalf("successful registered provider text = %q", text)
	}
	if got := receive(registeredRequest, "successful registered provider request"); got != "POST https://api.openai.com/v1/chat/completions" {
		t.Fatalf("successful registered provider request = %q", got)
	}
	if got := receive(authorization, "successful provider authorization"); got != "Bearer "+providerCredential {
		t.Fatal("successful provider request did not use the configured credential")
	}
	listedAfterSuccess, err := listHandler.ListItemProviderCallAudits(
		ctx,
		connect.NewRequest(&scribev1.ListItemProviderCallAuditsRequest{ItemId: itemID, Limit: 10}),
	)
	if err != nil {
		t.Fatalf("ListItemProviderCallAudits() after success error = %v", err)
	}
	if len(listedAfterSuccess.Msg.GetAudits()) != 2 {
		t.Fatalf("listed provider audits after success = %d, want 2", len(listedAfterSuccess.Msg.GetAudits()))
	}
	for _, persisted := range listedAfterSuccess.Msg.GetAudits() {
		if persisted.GetModel() != model {
			t.Fatalf("persisted provider audit model = %q, want registered model %q", persisted.GetModel(), model)
		}
	}

	logOutput := logs.String()
	listedAudit := listedAfterSuccess.Msg.String()
	for _, sentinel := range []string{providerCredential, responseContent, untrustedModel} {
		if strings.Contains(providerErr.Error(), sentinel) {
			t.Fatalf("returned provider error leaked %q: %q", sentinel, providerErr)
		}
		if strings.Contains(logOutput, sentinel) {
			t.Fatalf("provider log leaked %q: %s", sentinel, logOutput)
		}
		if strings.Contains(listedAudit, sentinel) {
			t.Fatalf("listed provider audit leaked %q: %s", sentinel, listedAudit)
		}
	}
	for _, diagnostic := range []string{
		`"msg":"provider call"`,
		`"provider":"openai"`,
		`"model":"gpt-4o"`,
		`"operation":"transcribe_image"`,
		`"http_status":503`,
		`"category":"provider"`,
		`"failure":"provider request failed with HTTP status 503"`,
	} {
		if !strings.Contains(logOutput, diagnostic) {
			t.Fatalf("provider log omitted safe diagnostic %s: %s", diagnostic, logOutput)
		}
	}
}
