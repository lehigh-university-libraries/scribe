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
)

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
	fixtureImageID, fixtureItemID, err := seedFixture(ctx, pool)
	if err != nil {
		slog.Error("seed browser fixture", "error_type", safelog.ErrorType(err))
		os.Exit(1)
	}
	defer cleanupFixture(pool, fixtureItemID)

	appHandler := server.NewHandler(
		store.NewOCRRunStore(pool),
		store.NewItemStore(pool),
		store.NewContextStore(pool),
		store.NewAnnotationStore(pool),
		store.NewTranscriptionJobStore(pool),
		nil,
		nil,
		nil,
	)
	appHandler.SetAppContext(ctx)
	fixtureMux := http.NewServeMux()
	fixtureMux.HandleFunc("GET /__browser-fixture/item-image-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(strconv.FormatUint(fixtureImageID, 10)))
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
	slog.Info("browser Connect fixture ready", "item_image_id", fixtureImageID)
	if err := httprun.Serve(ctx, httpServer, 5*time.Second); err != nil {
		slog.Error("serve browser Connect fixture", "error_type", safelog.ErrorType(err))
		os.Exit(1)
	}
}

func seedFixture(ctx context.Context, pool *sql.DB) (uint64, string, error) {
	fixtureItemID := "browser-connect-" + uuid.NewString()
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", fmt.Errorf("begin fixture transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO items (id, user_id, workspace_id, name, source_type, metadata)
		VALUES (?, 1, 1, 'Browser Connect CAS fixture', 'upload', JSON_OBJECT())
	`, fixtureItemID); err != nil {
		return 0, "", fmt.Errorf("insert fixture item: %w", err)
	}
	canvasURI := fixtureBaseURL + "/fixtures/canvas/1"
	imageResult, err := tx.ExecContext(ctx, `
		INSERT INTO item_images (workspace_id, item_id, sequence, image_url, canvas_uri, width, height, label)
		VALUES (1, ?, 0, 'https://images.example/fixture.jpg', ?, 1200, 600, 'Browser fixture')
	`, fixtureItemID, canvasURI)
	if err != nil {
		return 0, "", fmt.Errorf("insert fixture image: %w", err)
	}
	fixtureImageIDValue, err := imageResult.LastInsertId()
	if err != nil {
		return 0, "", fmt.Errorf("resolve fixture image id: %w", err)
	}
	if fixtureImageIDValue <= 0 {
		return 0, "", fmt.Errorf("resolve fixture image id: database returned %d", fixtureImageIDValue)
	}
	if err := tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("commit fixture resources: %w", err)
	}
	fixtureImageID := uint64(fixtureImageIDValue)

	pageID, err := iiif.CanonicalPageID(fixturePresentationBaseURL, fixtureImageID)
	if err != nil {
		cleanupFixture(pool, fixtureItemID)
		return 0, fixtureItemID, err
	}
	annotationID, err := iiif.AnnotationID(pageID, "browser-line-1")
	if err != nil {
		cleanupFixture(pool, fixtureItemID)
		return 0, fixtureItemID, err
	}
	identity := iiif.PageIdentity{
		PublicBaseURL: fixturePresentationBaseURL,
		ItemImageID:   fixtureImageID,
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
		cleanupFixture(pool, fixtureItemID)
		return 0, fixtureItemID, fmt.Errorf("encode fixture page: %w", err)
	}
	payload, err := iiif.NormalizeAnnotationPage(rawPage, identity)
	if err != nil {
		cleanupFixture(pool, fixtureItemID)
		return 0, fixtureItemID, fmt.Errorf("build fixture page: %w", err)
	}
	_, err = store.NewAnnotationStore(pool).SavePage(ctx, store.AnnotationPage{
		WorkspaceID: 1,
		ItemImageID: fixtureImageID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(payload),
	}, 0)
	if err != nil {
		cleanupFixture(pool, fixtureItemID)
		return 0, fixtureItemID, fmt.Errorf("save fixture page: %w", err)
	}
	return fixtureImageID, fixtureItemID, nil
}

func cleanupFixture(pool *sql.DB, fixtureItemID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, fixtureItemID); err != nil {
		slog.Warn("remove browser fixture", "error_type", safelog.ErrorType(err))
	}
}
