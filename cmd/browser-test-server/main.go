// Command browser-test-server is an isolated acceptance fixture that exposes
// Scribe's real Connect handler and MariaDB-backed canonical page repository to
// Playwright. It is used only by ci/test-browser.sh.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/database"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/httplimits"
	"github.com/lehigh-university-libraries/scribe/internal/httprun"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/safelog"
	"github.com/lehigh-university-libraries/scribe/internal/server"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const (
	fixtureBaseURL             = "http://scribe-browser-backend:8080"
	fixturePresentationBaseURL = fixtureBaseURL + "/presentation/v3"
	fixtureImageURL            = "http://127.0.0.1:4173/e2e/canvas-a.svg"
)

type browserFixture struct {
	ItemID      string
	ItemImageID uint64
	ContextID   uint64
}

type backgroundCompletionResponse struct {
	ItemImageID string `json:"itemImageId"`
	RemoteText  string `json:"remoteText"`
	Revision    string `json:"revision"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := strings.TrimSpace(os.Getenv("TEST_DSN"))
	if dsn == "" {
		slog.Error("TEST_DSN is required")
		os.Exit(2)
	}
	pool, err := database.NewPool(dsn, database.DefaultConfig())
	if err != nil {
		slog.Error("connect browser test database", "error_type", safelog.ErrorType(err))
		os.Exit(1)
	}
	defer pool.Close()
	if err := database.Migrate(pool); err != nil {
		slog.Error("initialize browser test database", "error_type", safelog.ErrorType(err))
		os.Exit(1)
	}

	config.Init(config.Runtime{Config: config.Config{
		ListenAddr:    ":8080",
		PublicBaseURL: fixtureBaseURL,
		CORS: config.CORSConfig{
			AllowedOrigins: []string{"http://127.0.0.1:4173"},
		},
		Pagination: config.PaginationConfig{
			SigningKey: strings.Repeat("browser-fixture-page-token-", 2),
		},
		Annotation: config.AnnotationConfig{
			APIBase:                 fixtureBaseURL,
			APIInternalBase:         fixtureBaseURL,
			TripletPresentationBase: fixturePresentationBaseURL,
		},
	}})
	fixture, err := seedFixture(ctx, pool)
	if err != nil {
		slog.Error("seed browser fixture", "error_type", safelog.ErrorType(err))
		os.Exit(1)
	}
	defer cleanupFixture(pool, fixture)

	ocrRuns := store.NewOCRRunStore(pool)
	items := store.NewItemStore(pool)
	contexts := store.NewContextStore(pool)
	annotations := store.NewAnnotationStore(pool)
	transcriptionJobs := store.NewTranscriptionJobStore(pool)
	appHandler := server.NewHandler(
		ocrRuns,
		items,
		contexts,
		annotations,
		transcriptionJobs,
		nil,
		nil,
		nil,
	)
	appHandler.SetAppContext(ctx)
	fixtureMux := http.NewServeMux()
	fixtureMux.HandleFunc("GET /__browser-fixture/item-image-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(strconv.FormatUint(fixture.ItemImageID, 10)))
	})
	fixtureMux.HandleFunc("POST /v1/__browser-fixture/complete-background-transcription", func(w http.ResponseWriter, r *http.Request) {
		result, completionErr := completeBackgroundTranscription(
			r.Context(),
			annotations,
			transcriptionJobs,
			fixture,
		)
		if completionErr != nil {
			slog.Error("complete browser background transcription", "error_type", safelog.ErrorType(completionErr))
			http.Error(w, "background completion fixture failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(w).Encode(result); encodeErr != nil {
			slog.Error("encode browser background completion", "error_type", safelog.ErrorType(encodeErr))
		}
	})
	fixtureMux.HandleFunc("POST /v1/__browser-fixture/reset-background-transcription", func(w http.ResponseWriter, r *http.Request) {
		if resetErr := resetBackgroundTranscription(
			r.Context(),
			contexts,
			annotations,
			transcriptionJobs,
			fixture,
		); resetErr != nil {
			slog.Error("reset browser background transcription", "error_type", safelog.ErrorType(resetErr))
			http.Error(w, "background reset fixture failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	fixtureMux.Handle("/", appHandler)
	httpServer := &http.Server{
		Addr:              ":8080",
		Handler:           fixtureMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    httplimits.MaxHeaderBytes,
	}
	slog.Info("browser Connect fixture ready", "item_image_id", fixture.ItemImageID)
	if err := httprun.Serve(ctx, httpServer, 5*time.Second); err != nil {
		slog.Error("serve browser Connect fixture", "error_type", safelog.ErrorType(err))
		os.Exit(1)
	}
}

func seedFixture(ctx context.Context, pool *sql.DB) (fixture browserFixture, returnErr error) {
	fixture.ItemID = "browser-connect-" + uuid.NewString()
	defer func() {
		if returnErr != nil {
			cleanupFixture(pool, fixture)
		}
	}()
	items := store.NewItemStore(pool)
	if _, err := items.Create(ctx, dbstore.CreateItemParams{
		ID:          fixture.ItemID,
		UserID:      store.AnonymousUserID,
		WorkspaceID: store.AnonymousWorkspaceID,
		Name:        "Browser Connect background fixture",
		SourceType:  "upload",
		Metadata:    "{}",
	}); err != nil {
		return fixture, fmt.Errorf("insert fixture item: %w", err)
	}
	canvasURI := fixtureBaseURL + "/fixtures/canvas/1"
	image, err := items.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID:    fixture.ItemID,
		Sequence:  0,
		ImageURL:  fixtureImageURL,
		CanvasURI: canvasURI,
		Width:     1200,
		Height:    600,
		Label:     "Browser fixture",
	})
	if err != nil {
		return fixture, fmt.Errorf("insert fixture image: %w", err)
	}
	fixture.ItemImageID = image.ID

	userID := store.AnonymousUserID
	workspaceID := store.AnonymousWorkspaceID
	processingContext, err := store.NewContextStore(pool).Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "Browser background " + uuid.NewString(),
		SegmentationModel:     "browser-fixture",
		TranscriptionProvider: "browser-fixture",
		TranscriptionModel:    "browser-fixture",
	})
	if err != nil {
		return fixture, fmt.Errorf("create fixture processing context: %w", err)
	}
	fixture.ContextID = processingContext.ID

	pageID, err := iiif.CanonicalPageID(fixturePresentationBaseURL, fixture.ItemImageID)
	if err != nil {
		return fixture, err
	}
	annotationID, err := iiif.AnnotationID(pageID, "browser-line-1")
	if err != nil {
		return fixture, err
	}
	identity := iiif.PageIdentity{
		PublicBaseURL: fixturePresentationBaseURL,
		ItemImageID:   fixture.ItemImageID,
		CanvasURI:     canvasURI,
	}
	rawPage, err := json.Marshal(map[string]any{
		"@context": []any{
			iiif.TextGranularityContext,
			"https://example.org/scribe-browser-extension/context.json",
			iiif.PresentationContext,
		},
		"type":           "AnnotationPage",
		"ex:pageCounter": json.Number("9007199254740995"),
		"items": []any{map[string]any{
			"id":              annotationID,
			"type":            "Annotation",
			"motivation":      "supplementing",
			"textGranularity": "line",
			"ex:largeInteger": json.Number("9007199254740993"),
			"ex:preciseDecimal": json.Number(
				"0.123456789012345678901",
			),
			"body": []any{map[string]any{
				"type":    "TextualBody",
				"purpose": "supplementing",
				"format":  "text/plain",
				"value":   "server base",
			}},
			"target": map[string]any{
				"type":   "SpecificResource",
				"source": map[string]any{"id": canvasURI, "type": "Canvas"},
				"selector": map[string]any{
					"type":       "FragmentSelector",
					"conformsTo": "http://www.w3.org/TR/media-frags/",
					"value":      "xywh=10,10,300,40",
				},
			},
		}},
	})
	if err != nil {
		return fixture, fmt.Errorf("encode fixture page: %w", err)
	}
	payload, err := iiif.NormalizeAnnotationPage(rawPage, identity)
	if err != nil {
		return fixture, fmt.Errorf("build fixture page: %w", err)
	}
	_, err = store.NewAnnotationStore(pool).SavePage(ctx, store.AnnotationPage{
		WorkspaceID: store.AnonymousWorkspaceID,
		ItemImageID: fixture.ItemImageID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(payload),
	}, 0)
	if err != nil {
		return fixture, fmt.Errorf("save fixture page: %w", err)
	}
	if err := store.NewOCRRunStore(pool).Create(ctx, store.OCRRun{
		SessionID:    "browser-background-" + uuid.NewString(),
		ItemImageID:  &fixture.ItemImageID,
		ContextID:    &fixture.ContextID,
		ImageURL:     fixtureImageURL,
		Provider:     "browser-fixture",
		Model:        "browser-fixture",
		OriginalHOCR: "",
		OriginalText: "server base",
	}); err != nil {
		return fixture, fmt.Errorf("create fixture OCR run: %w", err)
	}
	if _, err = store.NewTranscriptionJobStore(pool).Create(
		ctx,
		fixture.ItemImageID,
		processingContext,
	); err != nil {
		return fixture, fmt.Errorf("create fixture transcription job: %w", err)
	}
	return fixture, nil
}

func fixturePageWithText(
	page store.AnnotationPage,
	fixture browserFixture,
	text string,
) ([]byte, error) {
	var pageDocument map[string]any
	if err := iiif.DecodeJSON([]byte(page.Payload), &pageDocument); err != nil {
		return nil, fmt.Errorf("decode fixture page: %w", err)
	}
	items, ok := pageDocument["items"].([]any)
	if !ok || len(items) != 1 {
		return nil, fmt.Errorf("fixture page does not contain one annotation")
	}
	annotation, ok := items[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("fixture annotation is invalid")
	}
	bodies, ok := annotation["body"].([]any)
	if !ok || len(bodies) == 0 {
		return nil, fmt.Errorf("fixture annotation body is invalid")
	}
	body, ok := bodies[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("fixture annotation text body is invalid")
	}
	body["value"] = text
	rawPage, err := json.Marshal(pageDocument)
	if err != nil {
		return nil, fmt.Errorf("encode fixture page: %w", err)
	}
	normalized, err := iiif.NormalizeAnnotationPage(rawPage, iiif.PageIdentity{
		PublicBaseURL: fixturePresentationBaseURL,
		ItemImageID:   fixture.ItemImageID,
		CanvasURI:     page.CanvasURI,
	})
	if err != nil {
		return nil, fmt.Errorf("normalize fixture page: %w", err)
	}
	return normalized, nil
}

func resetBackgroundTranscription(
	ctx context.Context,
	contexts *store.ContextStore,
	annotations *store.AnnotationStore,
	jobs *store.TranscriptionJobStore,
	fixture browserFixture,
) error {
	const baseText = "server base"
	page, err := annotations.LoadPage(ctx, store.AnonymousWorkspaceID, fixture.ItemImageID)
	if err != nil {
		return fmt.Errorf("load fixture page for reset: %w", err)
	}
	normalized, err := fixturePageWithText(page, fixture, baseText)
	if err != nil {
		return err
	}
	if string(normalized) != page.Payload {
		page.Payload = string(normalized)
		page.UpdatedByUserID = nil
		page, err = annotations.SavePage(ctx, page, page.Revision)
		if err != nil {
			return fmt.Errorf("reset fixture page: %w", err)
		}
	}
	processingContext, err := contexts.GetForWorkspace(
		ctx,
		fixture.ContextID,
		store.AnonymousWorkspaceID,
	)
	if err != nil {
		return fmt.Errorf("load fixture processing context: %w", err)
	}
	if _, err := jobs.Create(ctx, fixture.ItemImageID, processingContext); err != nil {
		return fmt.Errorf("reset fixture transcription job at revision %d: %w", page.Revision, err)
	}
	return nil
}

func completeBackgroundTranscription(
	ctx context.Context,
	annotations *store.AnnotationStore,
	jobs *store.TranscriptionJobStore,
	fixture browserFixture,
) (backgroundCompletionResponse, error) {
	const remoteText = "worker result"
	page, err := annotations.LoadPage(ctx, store.AnonymousWorkspaceID, fixture.ItemImageID)
	if err != nil {
		return backgroundCompletionResponse{}, fmt.Errorf("load fixture page: %w", err)
	}
	normalized, err := fixturePageWithText(page, fixture, remoteText)
	if err != nil {
		return backgroundCompletionResponse{}, err
	}
	page.Payload = string(normalized)
	page.UpdatedByUserID = nil

	active, err := jobs.GetActiveByItemImage(ctx, fixture.ItemImageID)
	if err != nil {
		return backgroundCompletionResponse{}, fmt.Errorf("load active fixture transcription job: %w", err)
	}
	claimed, err := jobs.ClaimPendingByID(ctx, active.ID)
	if err != nil {
		return backgroundCompletionResponse{}, fmt.Errorf("claim fixture transcription job: %w", err)
	}
	if claimed == nil {
		return backgroundCompletionResponse{}, fmt.Errorf("claim fixture transcription job: no job returned")
	}
	fence, err := claimed.Fence()
	if err != nil {
		return backgroundCompletionResponse{}, fmt.Errorf("fence fixture transcription job: %w", err)
	}
	eventID := "browser-background-" + uuid.NewString()
	eventType := "dev.scribe.transcription.completed"
	subject := fmt.Sprintf("item-images/%d", fixture.ItemImageID)
	eventBody, err := json.Marshal(map[string]any{
		"specversion":     "1.0",
		"id":              eventID,
		"source":          "/scribe",
		"type":            eventType,
		"subject":         subject,
		"time":            time.Now().UTC().Format(time.RFC3339Nano),
		"datacontenttype": "application/json",
		"data": map[string]any{
			"jobId":             active.ID,
			"itemImageId":       fixture.ItemImageID,
			"completedSegments": 1,
			"failedSegments":    0,
			"totalSegments":     1,
		},
	})
	if err != nil {
		return backgroundCompletionResponse{}, fmt.Errorf("encode fixture completion event: %w", err)
	}
	saved, err := annotations.SavePageAndCompleteTranscriptionJob(
		ctx,
		page,
		page.Revision,
		store.AnnotationJobCompletion{
			TranscriptionAttemptFence: fence,
			EventID:                   eventID,
			EventType:                 eventType,
			Subject:                   subject,
			BodyJSON:                  string(eventBody),
		},
	)
	if err != nil {
		return backgroundCompletionResponse{}, fmt.Errorf("commit fixture transcription: %w", err)
	}
	return backgroundCompletionResponse{
		ItemImageID: strconv.FormatUint(fixture.ItemImageID, 10),
		RemoteText:  remoteText,
		Revision:    strconv.FormatUint(saved.Revision, 10),
	}, nil
}

func cleanupFixture(pool *sql.DB, fixture browserFixture) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if fixture.ItemID != "" {
		if err := store.NewItemStore(pool).DeleteForWorkspace(ctx, fixture.ItemID, store.AnonymousWorkspaceID); err != nil {
			slog.Warn("remove browser fixture item", "error_type", safelog.ErrorType(err))
		}
	}
	if fixture.ContextID > 0 {
		if err := store.NewContextStore(pool).DeleteForWorkspace(ctx, fixture.ContextID, store.AnonymousWorkspaceID); err != nil {
			slog.Warn("remove browser fixture context", "error_type", safelog.ErrorType(err))
		}
	}
}
