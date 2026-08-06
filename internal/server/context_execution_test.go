package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/worklimit"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

type contextExecutionTransportFunc func(*http.Request) (*http.Response, error)

func (f contextExecutionTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type contextExecutionOCR struct {
	*hocr.Service
}

type contextExecutionSecretResolver struct {
	secret store.ProviderSecret
}

func (r contextExecutionSecretResolver) ResolvePreferred(context.Context, uint64, *uint64, string) (store.ProviderSecret, error) {
	return r.secret, nil
}

type contextExecutionVault struct {
	credential string
}

func (v contextExecutionVault) Read(context.Context, string) (map[string]string, error) {
	return map[string]string{"api_key": v.credential}, nil
}

func (*contextExecutionOCR) ProcessImageURLWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected durable image processing")
}

func (*contextExecutionOCR) ProcessImageURLTransientWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected transient image processing")
}

func (*contextExecutionOCR) ProcessImageUploadWithContext(context.Context, string, []byte, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected upload processing")
}

func (*contextExecutionOCR) StoreUploadedImage(context.Context, string, []byte) (string, error) {
	return "", fmt.Errorf("unexpected upload storage")
}

func (o *contextExecutionOCR) TranscribeImageFileWithContext(ctx context.Context, imagePath, provider, model string) (string, error) {
	return o.TranscribeImageWithContext(ctx, imagePath, provider, model)
}

func TestNormalizeContextForExecutionDropsOnlyLegacyGemini3Temperature(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Gemini.Model = "gemini-3.5-flash"
	cfg.LLM.Gemini.Models = []string{"gemini-3.5-flash", "gemini-2.5-flash"}
	registry := providerregistry.New(cfg)
	legacyTemperature := 0.7
	legacySnapshot := store.Context{
		ID: 41, Name: "persisted Gemini 3 context", SegmentationModel: "scribe",
		TranscriptionProvider: "gemini", TranscriptionModel: "gemini-3.5-flash",
		SystemPrompt: "Preserve spelling.", Temperature: &legacyTemperature,
	}

	normalized, err := normalizeContextForExecution(legacySnapshot, registry)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Temperature != nil {
		t.Fatalf("normalized temperature = %v, want model default", normalized.Temperature)
	}
	if legacySnapshot.Temperature == nil || *legacySnapshot.Temperature != legacyTemperature {
		t.Fatalf("persisted snapshot was mutated: %+v", legacySnapshot)
	}
	if normalized.ID != legacySnapshot.ID || normalized.TranscriptionModel != legacySnapshot.TranscriptionModel || normalized.SystemPrompt != legacySnapshot.SystemPrompt {
		t.Fatalf("normalization changed non-temperature fields: %+v", normalized)
	}

	legacyModelContext := legacySnapshot
	legacyModelContext.TranscriptionModel = "gemini-2.5-flash"
	legacyNormalized, err := normalizeContextForExecution(legacyModelContext, registry)
	if err != nil {
		t.Fatal(err)
	}
	if legacyNormalized.Temperature == nil || *legacyNormalized.Temperature != legacyTemperature || legacyNormalized.Temperature == legacyModelContext.Temperature {
		t.Fatalf("Gemini 2.5 temperature = %v; want detached %v", legacyNormalized.Temperature, legacyTemperature)
	}
}

func TestNormalizeContextForExecutionStillRejectsInvalidSnapshots(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Gemini.Model = "gemini-3.5-flash"
	cfg.LLM.Gemini.Models = []string{"gemini-3.5-flash"}
	registry := providerregistry.New(cfg)
	temperature := 0.5

	tests := []store.Context{
		{TranscriptionProvider: "gemini", TranscriptionModel: "unregistered", Temperature: &temperature},
		{TranscriptionProvider: "unregistered", TranscriptionModel: "gemini-3.5-flash", Temperature: &temperature},
		{TranscriptionProvider: "tesseract", TranscriptionModel: "tesseract", SystemPrompt: "unsupported"},
	}
	for _, contextValue := range tests {
		if _, err := normalizeContextForExecution(contextValue, registry); err == nil {
			t.Fatalf("normalizeContextForExecution() accepted %+v", contextValue)
		}
	}
}

func TestForegroundAndBackgroundExecutePersistedGemini3ContextWithModelDefaultSampling(t *testing.T) {
	const model = "gemini-3.5-flash"
	previous := config.Get()
	configured := previous
	configured.Config.LLM.Gemini.Model = model
	configured.Config.LLM.Gemini.Models = []string{model, "gemini-2.5-flash"}
	configured.Config.Vault.Paths.ProviderSecrets = "scribe/test/provider-secrets/workspaces"
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	database := openTestDB(t)
	ctx := context.Background()
	contextStore := store.NewContextStore(database)
	legacyTemperature := 0.7
	userID := store.AnonymousUserID
	workspaceID := store.AnonymousWorkspaceID
	legacyContext, err := contextStore.Create(ctx, store.Context{
		UserID: &userID, WorkspaceID: &workspaceID,
		Name: uniqueName("legacy Gemini 3 enrichment"), SegmentationModel: "scribe",
		TranscriptionProvider: "gemini", TranscriptionModel: model,
		SystemPrompt: "Preserve the original spelling.", Temperature: &legacyTemperature,
	})
	if err != nil {
		t.Fatalf("persist legacy context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.DeleteForWorkspace(context.Background(), legacyContext.ID, workspaceID)
	})

	canvasURI := "https://source.example/canvas/" + uniqueName("legacy-gemini-enrichment")
	itemImage := createServerTestItemImage(t, database, workspaceID, userID, canvasURI)
	presentationBase := (&Handler{}).publicAnnotationBaseURL()
	pageID, err := iiif.CanonicalPageID(presentationBase, itemImage.ID)
	if err != nil {
		t.Fatalf("canonical page id: %v", err)
	}
	annotationID, err := iiif.AnnotationID(pageID, "legacy-gemini-line")
	if err != nil {
		t.Fatalf("canonical annotation id: %v", err)
	}
	annotation := transcriptionAnnotation(
		annotationID,
		"line",
		"before",
		canvasURI,
		models.BBox{X1: 0, Y1: 0, X2: 50, Y2: 20},
	)
	pageJSON, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: presentationBase,
		ItemImageID:   itemImage.ID,
		CanvasURI:     canvasURI,
	}, []any{annotation})
	if err != nil {
		t.Fatalf("build canonical page: %v", err)
	}
	annotationStore := store.NewAnnotationStore(database)
	if _, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: workspaceID,
		ItemImageID: itemImage.ID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(pageJSON),
	}, 0); err != nil {
		t.Fatalf("save canonical page: %v", err)
	}
	transcriptionJobs := store.NewTranscriptionJobStore(database)
	jobID, err := transcriptionJobs.Create(ctx, itemImage.ID, legacyContext)
	if err != nil {
		t.Fatalf("create legacy-context transcription job: %v", err)
	}
	annotationJSON, err := json.Marshal(annotation)
	if err != nil {
		t.Fatalf("encode annotation: %v", err)
	}

	var encoded bytes.Buffer
	region := image.NewRGBA(image.Rect(0, 0, 1, 1))
	region.Set(0, 0, color.White)
	if err := png.Encode(&encoded, region); err != nil {
		t.Fatalf("encode region image: %v", err)
	}
	regionPath := filepath.Join(t.TempDir(), "region.png")
	if err := os.WriteFile(regionPath, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write region image: %v", err)
	}

	var wireTemperatures []bool
	vendorClient := &http.Client{Transport: contextExecutionTransportFunc(func(request *http.Request) (*http.Response, error) {
		body, readErr := io.ReadAll(request.Body)
		_ = request.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(body)) != request.ContentLength {
			t.Fatalf("Gemini wire length = %d, want Content-Length %d", len(body), request.ContentLength)
		}
		var payload struct {
			GenerationConfig map[string]json.RawMessage `json:"generationConfig"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode Gemini request: %v", err)
		}
		_, hasTemperature := payload.GenerationConfig["temperature"]
		wireTemperatures = append(wireTemperatures, hasTemperature)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"modelVersion":"gemini-3.5-flash",
				"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"normalized transcription"}]}}]
			}`)),
			Request: request,
		}, nil
	})}
	limiter, err := worklimit.NewHierarchical(2, 2, 2)
	if err != nil {
		t.Fatalf("create processing limiter: %v", err)
	}
	handler := &Handler{
		items:             store.NewItemStore(database),
		contexts:          contextStore,
		annotations:       annotationStore,
		transcriptionJobs: transcriptionJobs,
		providerSecrets: contextExecutionSecretResolver{secret: store.ProviderSecret{
			ID:          71,
			WorkspaceID: workspaceID,
			Provider:    "gemini",
			Scope:       "workspace",
			VaultPath:   fmt.Sprintf("scribe/test/provider-secrets/workspaces/%d/gemini/key-1", workspaceID),
		}},
		vault: contextExecutionVault{credential: "test-gemini-credential"},
		ocr: &contextExecutionOCR{
			Service: hocr.NewService(providerregistry.WithVendorHTTPClient(vendorClient)),
		},
		processingLimiter: limiter,
		imageRegionFetcher: func(context.Context, string, int, int, int, int) (string, func(), error) {
			return regionPath, func() {}, nil
		},
	}
	requestContext := providerregistry.WithCredential(ctx, "gemini", "api_key", "test-gemini-credential")
	response, err := handler.EnrichAnnotation(requestContext, connect.NewRequest(&scribev1.EnrichAnnotationRequest{
		ItemImageId:    itemImage.ID,
		ContextId:      legacyContext.ID,
		Scope:          "line",
		AnnotationJson: string(annotationJSON),
	}))
	if err != nil {
		t.Fatalf("EnrichAnnotation legacy Gemini context: %v", err)
	}
	if len(wireTemperatures) != 1 || wireTemperatures[0] {
		t.Fatalf("foreground Gemini wire temperatures = %v; want [false]", wireTemperatures)
	}
	if !strings.Contains(response.Msg.GetAnnotationJson(), "normalized transcription") {
		t.Fatalf("enriched annotation = %s", response.Msg.GetAnnotationJson())
	}

	if err := handler.processQueuedTranscriptionJob(ctx, jobID); err != nil {
		t.Fatalf("process legacy-context transcription job: %v", err)
	}
	if len(wireTemperatures) != 2 || wireTemperatures[1] {
		t.Fatalf("foreground/background Gemini wire temperatures = %v; want [false false]", wireTemperatures)
	}
	completed, err := transcriptionJobs.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload legacy-context transcription job: %v", err)
	}
	if completed.Status != store.TranscriptionJobStatusCompleted || completed.CompletedSegments != 1 || completed.FailedSegments != 0 {
		t.Fatalf("completed legacy-context job = %+v", completed)
	}
}
