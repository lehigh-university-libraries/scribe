package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

type Handler struct {
	ocrRuns           *store.OCRRunStore
	items             *store.ItemStore
	contexts          *store.ContextStore
	annotations       *store.AnnotationStore
	transcriptionJobs *store.TranscriptionJobStore
	events            *eventBroker
	webhookClient     *http.Client
	webhookURLs       []string
	mux               http.Handler
	webDir            string
	ocr               *ocrhandlers.Handler
	// baseURL is derived from the first request; used for IIIF IDs.
	// The annotation handler needs it to build annotation item URLs.
	annotationBaseURL string
}

type processProgress struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	Done      bool      `json:"done"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var (
	progressMu    sync.RWMutex
	progressState = map[string]processProgress{}
)

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *responseWriter) Flush() {
	flusher, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()
}

func AccessLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w}

		next.ServeHTTP(wrapped, r)
		if wrapped.statusCode == 0 {
			wrapped.statusCode = http.StatusOK
		}

		slog.Info(r.Method+" "+r.URL.Path,
			"status", wrapped.statusCode,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

func NewHandler(
	ocrRuns *store.OCRRunStore,
	items *store.ItemStore,
	contexts *store.ContextStore,
	annotations *store.AnnotationStore,
	transcriptionJobs *store.TranscriptionJobStore,
) *Handler {
	webDir := detectWebDir()
	if webDir == "" {
		slog.Warn("web assets directory not found; root path will return 404")
	} else {
		slog.Info("serving web assets", "dir", webDir)
	}

	handler := &Handler{
		ocrRuns:           ocrRuns,
		items:             items,
		contexts:          contexts,
		annotations:       annotations,
		transcriptionJobs: transcriptionJobs,
		events:            newEventBroker(),
		webhookClient:     &http.Client{Timeout: 10 * time.Second},
		webhookURLs:       parseWebhookURLs(os.Getenv("SCRIBE_WEBHOOK_URLS")),
		webDir:            webDir,
		ocr:               ocrhandlers.New(),
	}
	mux := http.NewServeMux()

	// Connect RPC services
	imageAPIPath, imageAPIHandler := scribev1connect.NewImageProcessingServiceHandler(handler)
	mux.Handle(imageAPIPath, imageAPIHandler)
	itemAPIPath, itemAPIHandler := scribev1connect.NewItemServiceHandler(handler)
	mux.Handle(itemAPIPath, itemAPIHandler)
	contextAPIPath, contextAPIHandler := scribev1connect.NewContextServiceHandler(handler)
	mux.Handle(contextAPIPath, contextAPIHandler)
	annotationAPIPath, annotationAPIHandler := scribev1connect.NewAnnotationServiceHandler(handler)
	mux.Handle(annotationAPIPath, annotationAPIHandler)
	transcriptionAPIPath, transcriptionAPIHandler := scribev1connect.NewTranscriptionServiceHandler(handler)
	mux.Handle(transcriptionAPIPath, transcriptionAPIHandler)

	// Health
	mux.HandleFunc("GET /healthz", handler.handleHealth)

	// IIIF presentation endpoints used by the editor.
	mux.HandleFunc("GET /v1/item-images/{item_image_id}/manifest", handler.handleGetIIIFManifest)
	mux.HandleFunc("GET /v1/item-images/{item_image_id}/annotations", handler.handleGetIIIFAnnotations)
	mux.HandleFunc("GET /v1/item-images/{item_image_id}/hocr", handler.handleGetHOCR)
	mux.HandleFunc("GET /v1/item-images/{item_image_id}/export", handler.handleExportAnnotations)
	mux.HandleFunc("GET /v1/events", handler.handleEventStream)
	mux.HandleFunc("POST /scribe.v1.AnnotationService/PublishItemImageEdits", handler.handlePublishItemImageEdits)

	// Context metrics
	mux.HandleFunc("GET /v1/contexts/{context_id}/metrics", handler.handleGetContextMetrics)

	// Static assets
	mux.Handle("GET /static/uploads/", http.StripPrefix("/static/uploads/", http.FileServer(http.Dir("uploads"))))
	mux.HandleFunc("/", handler.handleWeb)
	handler.mux = AccessLogger(mux)
	return handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS: allow all origins for annotation / Connect RPC clients.
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = "*"
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Accept,Authorization,Connect-Protocol-Version,X-Provider")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleGetHOCR(w http.ResponseWriter, r *http.Request) {
	run, _, _, _, err := h.resolveRunAndIIIFPaths(r)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hocrXML := strings.TrimSpace(run.OriginalHOCR)
	if run.CorrectedHOCR != nil && strings.TrimSpace(*run.CorrectedHOCR) != "" {
		hocrXML = strings.TrimSpace(*run.CorrectedHOCR)
	}
	if persisted, ok := readPreferredSessionHOCR(run.SessionID); ok {
		hocrXML = persisted
	}
	if hocrXML == "" {
		writeError(w, http.StatusNotFound, "hocr not found")
		return
	}
	w.Header().Set("Content-Type", "text/vnd.hocr+html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(hocrXML))
}

func (h *Handler) handleGetIIIFManifest(w http.ResponseWriter, r *http.Request) {
	itemImageID, err := itemImageIDFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	run, manifestPath, annotationsPath, hocrPath, err := h.resolveRunAndIIIFPaths(r)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hocrXML := strings.TrimSpace(run.OriginalHOCR)
	if run.CorrectedHOCR != nil && strings.TrimSpace(*run.CorrectedHOCR) != "" {
		hocrXML = strings.TrimSpace(*run.CorrectedHOCR)
	}
	if persisted, ok := readPreferredSessionHOCR(run.SessionID); ok {
		hocrXML = persisted
	}
	pageW, pageH := extractPageDimensions(hocrXML)
	if pageW <= 0 {
		pageW = 1
	}
	if pageH <= 0 {
		pageH = 1
	}

	apiBase := requestOrigin(r)
	manifestID := apiBase + manifestPath
	canvasID := fmt.Sprintf("%s/canvas/page-1", manifestID)
	paintingPageID := fmt.Sprintf("%s/page/painting", manifestID)
	paintingAnnID := fmt.Sprintf("%s/annotation/painting-1", manifestID)
	annotationPageURI := apiBase + annotationsPath
	seeAlsoID := apiBase + hocrPath

	img, err := h.items.GetImage(r.Context(), itemImageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "item image not found")
		return
	}
	if strings.TrimSpace(img.CanvasURI) == "" {
		if err := h.items.UpdateImageCanvasURI(r.Context(), itemImageID, canvasID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist canvas uri")
			return
		}
	}

	iiifBase := resolvePublicBase(os.Getenv("CANTALOUPE_IIIF_BASE"), r, "/cantaloupe/iiif/2")
	imageBody := buildImageBody(run.ImageURL, iiifBase, pageW, pageH)
	canvasLabel := run.ImageURL
	if iiifID, err := iiifIdentifierFromImageURL(run.ImageURL); err == nil {
		canvasLabel = iiifID
	}

	manifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/3/context.json",
		"id":       manifestID,
		"type":     "Manifest",
		"label": map[string]any{
			"none": []string{iiifManifestLabel(run)},
		},
		"items": []any{
			map[string]any{
				"id":     canvasID,
				"type":   "Canvas",
				"label":  map[string]any{"none": []string{canvasLabel}},
				"height": pageH,
				"width":  pageW,
				"items": []any{
					map[string]any{
						"id":   paintingPageID,
						"type": "AnnotationPage",
						"items": []any{
							map[string]any{
								"id":         paintingAnnID,
								"type":       "Annotation",
								"motivation": "painting",
								"target":     canvasID,
								"body":       imageBody,
							},
						},
					},
				},
				"annotations": []any{
					map[string]any{
						"id":   annotationPageURI,
						"type": "AnnotationPage",
					},
				},
				"seeAlso": []any{
					map[string]any{
						"id":      seeAlsoID,
						"type":    "Text",
						"format":  "text/vnd.hocr+html",
						"profile": "http://kba.cloud/hocr-spec",
						"label":   map[string]any{"none": []string{"hOCR embedded text"}},
					},
				},
			},
		},
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (h *Handler) handleGetIIIFAnnotations(w http.ResponseWriter, r *http.Request) {
	run, manifestPath, annotationsPath, _, err := h.resolveRunAndIIIFPaths(r)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hocrXML := strings.TrimSpace(run.OriginalHOCR)
	if run.CorrectedHOCR != nil && strings.TrimSpace(*run.CorrectedHOCR) != "" {
		hocrXML = strings.TrimSpace(*run.CorrectedHOCR)
	}
	if persisted, ok := readPreferredSessionHOCR(run.SessionID); ok {
		hocrXML = persisted
	}
	if hocrXML == "" {
		writeError(w, http.StatusNotFound, "hocr not found")
		return
	}

	granularity := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("textGranularity")))
	if granularity == "" {
		granularity = "line"
	}
	if granularity != "line" && granularity != "word" && granularity != "glyph" {
		writeError(w, http.StatusBadRequest, "textGranularity must be one of: line, word, glyph")
		return
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	apiBase := scheme + "://" + r.Host
	manifestID := apiBase + manifestPath
	canvasID := fmt.Sprintf("%s/canvas/page-1", manifestID)
	pageID := fmt.Sprintf("%s%s?textGranularity=%s", apiBase, annotationsPath, granularity)
	annotationScopeID := run.SessionID
	if run.ItemImageID != nil {
		annotationScopeID = fmt.Sprintf("item-image-%d", *run.ItemImageID)
	}

	var items []any
	switch granularity {
	case "line":
		lines, err := hocr.ParseHOCRLines(hocrXML)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unable to parse hocr lines")
			return
		}
		items = buildLineAnnotations(annotationScopeID, canvasID, lines)
	case "word":
		words, err := hocr.ParseHOCRWords(hocrXML)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unable to parse hocr words")
			return
		}
		items = buildWordAnnotations(annotationScopeID, canvasID, words)
	case "glyph":
		wordGlyphs, err := hocr.ParseHOCRWordGlyphs(hocrXML)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unable to parse hocr glyphs")
			return
		}
		items = buildGlyphAnnotations(annotationScopeID, canvasID, wordGlyphs)
	}

	payload := map[string]any{
		"@context": annotationPageContexts(),
		"id":       pageID,
		"type":     "AnnotationPage",
		"items":    items,
	}
	writeJSON(w, http.StatusOK, payload)
}

func joinLineWords(line models.HOCRLine) string {
	if len(line.Words) == 0 {
		return line.ID
	}
	parts := make([]string, 0, len(line.Words))
	for _, word := range line.Words {
		txt := strings.TrimSpace(word.Text)
		if txt == "" {
			continue
		}
		parts = append(parts, txt)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func buildLineAnnotations(sessionID, canvasID string, lines []models.HOCRLine) []any {
	items := make([]any, 0, len(lines))
	for i, line := range lines {
		width := line.BBox.X2 - line.BBox.X1
		height := line.BBox.Y2 - line.BBox.Y1
		if width <= 0 || height <= 0 {
			continue
		}
		text := strings.TrimSpace(joinLineWords(line))
		lineID := strings.TrimSpace(line.ID)
		if lineID == "" {
			lineID = fmt.Sprintf("line-%d", i+1)
		}
		annID := annotationID(sessionID, "line", lineID)
		items = append(items, transcriptionAnnotation(annID, "line", text, canvasID, line.BBox))
	}
	return items
}

func buildWordAnnotations(sessionID, canvasID string, words []models.HOCRWord) []any {
	items := make([]any, 0, len(words))
	for i, word := range words {
		width := word.BBox.X2 - word.BBox.X1
		height := word.BBox.Y2 - word.BBox.Y1
		if width <= 0 || height <= 0 {
			continue
		}
		wordID := strings.TrimSpace(word.ID)
		if wordID == "" {
			wordID = fmt.Sprintf("word-%d", i+1)
		}
		annID := annotationID(sessionID, "word", wordID)
		items = append(items, transcriptionAnnotation(annID, "word", strings.TrimSpace(word.Text), canvasID, word.BBox))
	}
	return items
}

func buildGlyphAnnotations(sessionID, canvasID string, wordGlyphs []hocr.WordWithGlyphs) []any {
	items := make([]any, 0)
	count := 0
	for _, ww := range wordGlyphs {
		for _, glyph := range ww.Glyphs {
			width := glyph.BBox.X2 - glyph.BBox.X1
			height := glyph.BBox.Y2 - glyph.BBox.Y1
			if width <= 0 || height <= 0 {
				continue
			}
			count++
			glyphID := strings.TrimSpace(glyph.ID)
			if glyphID == "" {
				glyphID = fmt.Sprintf("%s-glyph-%d", ww.Word.ID, count)
			}
			annID := annotationID(sessionID, "glyph", glyphID)
			items = append(items, transcriptionAnnotation(annID, "glyph", strings.TrimSpace(glyph.Text), canvasID, glyph.BBox))
		}
	}
	return items
}

func transcriptionAnnotation(id, granularity, text, canvasID string, box models.BBox) map[string]any {
	width := box.X2 - box.X1
	height := box.Y2 - box.Y1
	return map[string]any{
		"id":              id,
		"type":            "Annotation",
		"textGranularity": granularity,
		"motivation":      "supplementing",
		"body": []any{
			map[string]any{
				"type":    "TextualBody",
				"purpose": "supplementing",
				"format":  "text/plain",
				"value":   text,
			},
		},
		"target": map[string]any{
			"source": map[string]any{
				"id":   canvasID,
				"type": "Canvas",
			},
			"selector": map[string]any{
				"type":       "FragmentSelector",
				"conformsTo": "http://www.w3.org/TR/media-frags/",
				"value":      fmt.Sprintf("xywh=%d,%d,%d,%d", box.X1, box.Y1, width, height),
			},
		},
	}
}

func iiifManifestLabel(run store.OCRRun) string {
	if run.ItemImageID != nil {
		return fmt.Sprintf("item-image-%d", *run.ItemImageID)
	}
	return run.SessionID
}

// fetchOrCacheHOCRRun returns an OCRRun for the given item_image_id. If no run
// exists yet, it fetches hOCR on-demand from the item_image's hocr_url, caches
// the result as a new OCR run, and returns it.
func (h *Handler) fetchOrCacheHOCRRun(ctx context.Context, itemImageID uint64) (store.OCRRun, error) {
	run, err := h.ocrRuns.GetByItemImageID(ctx, itemImageID)
	if err == nil {
		return run, nil
	}
	if err != sql.ErrNoRows && !strings.Contains(err.Error(), "no rows") {
		return store.OCRRun{}, err
	}
	// No OCR run yet — try to fetch and cache hOCR from the item_image's hocr_url.
	img, imgErr := h.items.GetImage(ctx, itemImageID)
	if imgErr != nil {
		return store.OCRRun{}, fmt.Errorf("item image not found")
	}
	if img.HocrURL == "" {
		return store.OCRRun{}, fmt.Errorf("item image not found")
	}
	hocrXML, fetchErr := fetchHOCRContent(ctx, img.HocrURL)
	if fetchErr != nil || strings.TrimSpace(hocrXML) == "" {
		slog.Warn("on-demand hOCR fetch failed", "item_image_id", itemImageID, "hocr_url", img.HocrURL, "error", fetchErr)
		return store.OCRRun{}, fmt.Errorf("item image not found")
	}
	hocrXML = strings.TrimSpace(hocrXML)
	sessionID := fmt.Sprintf("hocr-url-%d", itemImageID)
	plainText := hocrToPlainTextLenient(hocrXML)
	run = store.OCRRun{
		SessionID:    sessionID,
		ItemImageID:  &itemImageID,
		ImageURL:     img.ImageURL,
		Provider:     "manifest",
		Model:        "imported",
		OriginalHOCR: hocrXML,
		OriginalText: plainText,
	}
	if cacheErr := h.ocrRuns.Create(ctx, run); cacheErr != nil {
		slog.Warn("failed to cache on-demand hOCR run", "item_image_id", itemImageID, "error", cacheErr)
	}
	return run, nil
}

func (h *Handler) resolveRunAndIIIFPaths(r *http.Request) (store.OCRRun, string, string, string, error) {
	ctx := r.Context()

	itemImageID, err := itemImageIDFromRequest(r)
	if err != nil {
		return store.OCRRun{}, "", "", "", err
	}
	run, err := h.fetchOrCacheHOCRRun(ctx, itemImageID)
	if err != nil {
		return store.OCRRun{}, "", "", "", err
	}
	base := fmt.Sprintf("/v1/item-images/%d", itemImageID)
	return run, base + "/manifest", base + "/annotations", base + "/hocr", nil
}

func itemImageIDFromRequest(r *http.Request) (uint64, error) {
	itemImageIDRaw := strings.TrimSpace(r.PathValue("item_image_id"))
	if itemImageIDRaw == "" {
		return 0, fmt.Errorf("item_image_id is required")
	}
	itemImageID, err := strconv.ParseUint(itemImageIDRaw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid item_image_id")
	}
	return itemImageID, nil
}

func (h *Handler) internalAnnotationBaseURL() string {
	base := strings.TrimRight(strings.TrimSpace(h.annotationBaseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("ANNOTATION_API_BASE")), "/")
	}
	if base == "" {
		base = "http://localhost:8080"
	}
	return base
}

func (h *Handler) ensureItemImageCanvasAndAnnotations(ctx context.Context, run store.OCRRun, itemImageID uint64) error {
	img, err := h.items.GetImage(ctx, itemImageID)
	if err != nil {
		return fmt.Errorf("get item image: %w", err)
	}

	canvasURI := strings.TrimSpace(img.CanvasURI)
	if canvasURI == "" {
		canvasURI = fmt.Sprintf("%s/v1/item-images/%d/manifest/canvas/page-1", h.internalAnnotationBaseURL(), itemImageID)
		if err := h.items.UpdateImageCanvasURI(ctx, itemImageID, canvasURI); err != nil {
			return fmt.Errorf("persist canvas uri: %w", err)
		}
	}

	existing, err := h.annotations.SearchByCanvas(ctx, canvasURI)
	if err != nil {
		return fmt.Errorf("search annotations: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}

	hocrXML := strings.TrimSpace(run.OriginalHOCR)
	if run.CorrectedHOCR != nil && strings.TrimSpace(*run.CorrectedHOCR) != "" {
		hocrXML = strings.TrimSpace(*run.CorrectedHOCR)
	}
	if persisted, ok := readPreferredSessionHOCR(run.SessionID); ok {
		hocrXML = persisted
	}
	if hocrXML == "" {
		return nil
	}

	annotationScopeID := run.SessionID
	if run.ItemImageID != nil {
		annotationScopeID = fmt.Sprintf("item-image-%d", *run.ItemImageID)
	}

	lines, err := hocr.ParseHOCRLines(hocrXML)
	if err != nil {
		return fmt.Errorf("parse hocr lines: %w", err)
	}
	words, err := hocr.ParseHOCRWords(hocrXML)
	if err != nil {
		return fmt.Errorf("parse hocr words: %w", err)
	}

	items := append(buildLineAnnotations(annotationScopeID, canvasURI, lines), buildWordAnnotations(annotationScopeID, canvasURI, words)...)
	if _, err := h.persistAnnotationItems(ctx, canvasURI, items); err != nil {
		return fmt.Errorf("persist annotations: %w", err)
	}
	h.publishEvent("dev.scribe.annotations.created", subjectForItemImage(itemImageID), map[string]any{
		"itemImageId":      itemImageID,
		"canvasUri":        canvasURI,
		"annotationCount":  len(items),
		"annotationPageId": annotationPageID(canvasURI),
	})
	return nil
}

func iiifBaseURL() string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("CANTALOUPE_IIIF_INTERNAL_BASE")), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("CANTALOUPE_IIIF_BASE")), "/")
	}
	if base == "" {
		base = "http://cantaloupe:8182/iiif/2"
	}
	return base
}

func fetchIIIFImageToTemp(iiifID string) (string, func(), error) {
	imageURL := fmt.Sprintf("%s/%s/full/full/0/default.jpg", iiifBaseURL(), iiifID)
	resp, err := http.Get(imageURL) // #nosec G107 - IIIF base comes from trusted environment config
	if err != nil {
		return "", func() {}, fmt.Errorf("fetch iiif image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", func() {}, fmt.Errorf("fetch iiif image: status %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", "scribe-image-*.jpg")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp image file: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", func() {}, fmt.Errorf("write temp image file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", func() {}, fmt.Errorf("close temp image file: %w", err)
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

func fetchIIIFRegionToTemp(iiifID string, x1, y1, x2, y2 int) (string, func(), error) {
	width := x2 - x1
	height := y2 - y1
	if width <= 0 || height <= 0 {
		return "", func() {}, fmt.Errorf("invalid bbox")
	}
	cropURL := fmt.Sprintf("%s/%s/%d,%d,%d,%d/full/0/default.jpg", iiifBaseURL(), iiifID, x1, y1, width, height)
	resp, err := http.Get(cropURL) // #nosec G107 - IIIF base comes from trusted environment config
	if err != nil {
		return "", func() {}, fmt.Errorf("fetch iiif crop: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", func() {}, fmt.Errorf("fetch iiif crop: status %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", "scribe-region-*.jpg")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp crop file: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", func() {}, fmt.Errorf("write temp crop file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", func() {}, fmt.Errorf("close temp crop file: %w", err)
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

func (h *Handler) startAsyncTranscription(sessionID, imageURL, provider, model string) {
	go func() {
		ctx := context.Background()
		run, err := h.ocrRuns.Get(ctx, sessionID)
		if err != nil {
			slog.Warn("Skipping async transcription; session lookup failed", "session_id", sessionID, "error", err)
			return
		}
		sourceHOCR := strings.TrimSpace(run.OriginalHOCR)
		if persisted, ok := readSessionHOCR(sessionID, "original.hocr"); ok && strings.TrimSpace(persisted) != "" {
			sourceHOCR = persisted
		}
		if sourceHOCR == "" {
			slog.Warn("Skipping async transcription; missing source hOCR", "session_id", sessionID)
			return
		}
		lines, err := hocr.ParseHOCRLines(sourceHOCR)
		if err != nil {
			slog.Warn("Skipping async transcription; unable to parse source hOCR", "session_id", sessionID, "error", err)
			return
		}
		if len(lines) == 0 {
			slog.Warn("Skipping async transcription; no detected lines", "session_id", sessionID)
			return
		}

		iiifID, err := iiifIdentifierFromImageURL(imageURL)
		if err != nil {
			slog.Warn("Skipping async transcription; invalid IIIF identifier", "session_id", sessionID, "error", err)
			return
		}
		slog.Info(
			"Starting async session transcription",
			"session_id", sessionID,
			"provider", effectiveProvider(provider),
			"model", effectiveModel(provider, model),
			"line_count", len(lines),
		)

		type lineJob struct {
			idx  int
			line models.HOCRLine
		}
		type lineResult struct {
			idx  int
			line models.HOCRLine
		}
		jobs := make(chan lineJob, len(lines))
		results := make(chan lineResult, len(lines))
		var wg sync.WaitGroup

		workerCount := getAsyncTranscribeConcurrency()
		if workerCount > len(lines) {
			workerCount = len(lines)
		}
		if workerCount < 1 {
			workerCount = 1
		}

		worker := func() {
			defer wg.Done()
			for job := range jobs {
				outLine := job.line
				width := outLine.BBox.X2 - outLine.BBox.X1
				height := outLine.BBox.Y2 - outLine.BBox.Y1
				if width <= 0 || height <= 0 {
					outLine.Words = nil
					results <- lineResult{idx: job.idx, line: outLine}
					continue
				}

				regionPath, cleanup, err := fetchIIIFRegionToTemp(iiifID, outLine.BBox.X1, outLine.BBox.Y1, outLine.BBox.X2, outLine.BBox.Y2)
				if err != nil {
					slog.Warn("Async line fetch failed", "session_id", sessionID, "line_id", outLine.ID, "error", err)
					outLine.Words = nil
					results <- lineResult{idx: job.idx, line: outLine}
					continue
				}
				text, err := h.ocr.TranscribeImageFile(regionPath, provider, model)
				cleanup()
				if err != nil {
					slog.Warn("Async line transcription failed", "session_id", sessionID, "line_id", outLine.ID, "error", err)
					outLine.Words = nil
					results <- lineResult{idx: job.idx, line: outLine}
					continue
				}
				outLine.Words = []models.HOCRWord{
					{
						ID:         fmt.Sprintf("word_%d_0", job.idx),
						LineID:     outLine.ID,
						BBox:       outLine.BBox,
						Text:       text,
						Confidence: 85,
					},
				}
				results <- lineResult{idx: job.idx, line: outLine}
			}
		}

		wg.Add(workerCount)
		for i := 0; i < workerCount; i++ {
			go worker()
		}
		for i, line := range lines {
			jobs <- lineJob{idx: i, line: line}
		}
		close(jobs)
		wg.Wait()
		close(results)

		rebuilt := make([]models.HOCRLine, len(lines))
		for result := range results {
			rebuilt[result.idx] = result.line
		}

		pageW, pageH := extractPageDimensions(sourceHOCR)
		if pageW <= 0 || pageH <= 0 {
			for _, line := range rebuilt {
				if line.BBox.X2 > pageW {
					pageW = line.BBox.X2
				}
				if line.BBox.Y2 > pageH {
					pageH = line.BBox.Y2
				}
			}
		}
		if pageW <= 0 {
			pageW = 1
		}
		if pageH <= 0 {
			pageH = 1
		}

		converter := hocr.NewConverter()
		hocrXML := converter.ConvertHOCRLinesToXML(rebuilt, pageW, pageH)

		plainText, err := ocrhandlers.HOCRToPlainText(hocrXML)
		if err != nil {
			plainText = hocrToPlainTextLenient(hocrXML)
		}

		if err := h.ocrRuns.Create(ctx, store.OCRRun{
			SessionID:    sessionID,
			ImageURL:     imageURL,
			Provider:     effectiveProvider(provider),
			Model:        effectiveModel(provider, model),
			OriginalHOCR: hocrXML,
			OriginalText: plainText,
		}); err != nil {
			slog.Warn("Async session transcription save failed", "session_id", sessionID, "error", err)
			return
		}
		if err := writeSessionHOCR(sessionID, "original.hocr", hocrXML); err != nil {
			slog.Warn("Async session transcription persist failed", "session_id", sessionID, "error", err)
			return
		}
		slog.Info("Async session transcription complete", "session_id", sessionID)
	}()
}

func getAsyncTranscribeConcurrency() int {
	raw := strings.TrimSpace(os.Getenv("LINE_TRANSCRIBE_CONCURRENCY"))
	if raw == "" {
		return 5
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 5
	}
	return v
}

// buildImageBody returns a IIIF Presentation v3 painting body for the given image URL.
// For local uploads it wraps the image in a IIIF Image Service descriptor.
// For external URLs it detects and reuses any IIIF image service embedded in the URL;
// otherwise it returns a plain Image body.
func buildImageBody(imageURL, iiifBase string, pageW, pageH int) map[string]any {
	body := map[string]any{
		"type":   "Image",
		"height": pageH,
		"width":  pageW,
	}

	// Local upload: use our own Cantaloupe IIIF service.
	if strings.HasPrefix(imageURL, "/static/uploads/") {
		iiifID, err := iiifIdentifierFromImageURL(imageURL)
		if err == nil {
			serviceID := iiifBase + "/" + iiifID
			body["id"] = serviceID + "/full/full/0/default.jpg"
			body["format"] = "image/jpeg"
			body["service"] = []any{map[string]any{
				"id":      serviceID,
				"type":    "ImageService2",
				"profile": "http://iiif.io/api/image/2/level2.json",
			}}
			return body
		}
	}

	// External URL: use as-is and try to attach a IIIF service descriptor.
	body["id"] = imageURL
	body["format"] = "image/jpeg"
	if serviceID := iiifServiceFromImageURL(imageURL); serviceID != "" {
		body["service"] = []any{map[string]any{
			"id":      serviceID,
			"type":    "ImageService2",
			"profile": "http://iiif.io/api/image/2/level2.json",
		}}
	}
	return body
}

// iiifServiceFromImageURL extracts the IIIF image service base URL from a full
// IIIF image URL by stripping the trailing region/size/rotation/quality segments.
// Returns "" if the URL does not appear to be a IIIF image URL.
func iiifServiceFromImageURL(imageURL string) string {
	for _, seg := range []string{"/iiif/2/", "/iiif/3/"} {
		if !strings.Contains(imageURL, seg) {
			continue
		}
		// Strip the last 4 path segments (region/size/rotation/quality.format).
		u := imageURL
		for i := 0; i < 4; i++ {
			idx := strings.LastIndex(u, "/")
			if idx < 0 {
				return ""
			}
			u = u[:idx]
		}
		return u
	}
	return ""
}

func iiifIdentifierFromImageURL(imageURL string) (string, error) {
	u := strings.TrimSpace(imageURL)
	if u == "" {
		return "", fmt.Errorf("session has no image")
	}
	const staticPrefix = "/static/uploads/"
	if strings.HasPrefix(u, staticPrefix) {
		name := strings.TrimPrefix(u, staticPrefix)
		if strings.TrimSpace(name) == "" {
			return "", fmt.Errorf("invalid image path")
		}
		return url.PathEscape(name), nil
	}
	return "", fmt.Errorf("manifest requires a local uploaded image")
}

func effectiveModel(provider, requestModel string) string {
	if strings.TrimSpace(requestModel) != "" {
		return strings.TrimSpace(requestModel)
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	}
	if provider == "" {
		provider = "ollama"
	}
	switch provider {
	case "openai":
		if m := strings.TrimSpace(os.Getenv("OPENAI_MODEL")); m != "" {
			return m
		}
		return "gpt-4o"
	case "gemini":
		if m := strings.TrimSpace(os.Getenv("GEMINI_MODEL")); m != "" {
			return m
		}
		return "gemini-2.0-flash"
	default:
		if m := strings.TrimSpace(os.Getenv("OLLAMA_MODEL")); m != "" {
			return m
		}
		return "mistral-small3.2:24b"
	}
}

func effectiveProvider(requestProvider string) string {
	p := strings.ToLower(strings.TrimSpace(requestProvider))
	if p != "" {
		return p
	}
	env := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	if env != "" {
		return env
	}
	return "ollama"
}

type bbox struct {
	x1 int
	y1 int
	x2 int
	y2 int
}

type boxEditMetrics struct {
	ChangedCount int
	AddedCount   int
	DeletedCount int
	ChangeScore  float64
}

func calculateBoxEditMetrics(originalHOCR, correctedHOCR string) boxEditMetrics {
	origLines, _ := ocrhandlers.HOCRToLines(originalHOCR)
	newLines, _ := ocrhandlers.HOCRToLines(correctedHOCR)
	origPageW, origPageH := extractPageDimensions(originalHOCR)
	newPageW, newPageH := extractPageDimensions(correctedHOCR)
	pageW := maxInt(origPageW, newPageW)
	pageH := maxInt(origPageH, newPageH)
	if pageW <= 0 {
		pageW = 1
	}
	if pageH <= 0 {
		pageH = 1
	}

	origMap := make(map[string]bbox, len(origLines))
	for _, line := range origLines {
		origMap[line.ID] = bbox{line.BBox.X1, line.BBox.Y1, line.BBox.X2, line.BBox.Y2}
	}
	newMap := make(map[string]bbox, len(newLines))
	for _, line := range newLines {
		newMap[line.ID] = bbox{line.BBox.X1, line.BBox.Y1, line.BBox.X2, line.BBox.Y2}
	}

	ids := make([]string, 0, len(origMap)+len(newMap))
	seen := map[string]bool{}
	for id := range origMap {
		ids = append(ids, id)
		seen[id] = true
	}
	for id := range newMap {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	changed := 0
	added := 0
	deleted := 0
	totalScore := 0.0

	for _, id := range ids {
		ob, hasOrig := origMap[id]
		nb, hasNew := newMap[id]

		if hasOrig && !hasNew {
			deleted++
			totalScore += 1.0
			continue
		}
		if !hasOrig && hasNew {
			added++
			totalScore += 1.0
			continue
		}

		score := boxDeltaScore(ob, nb, pageW, pageH)
		if score > 0 {
			changed++
			totalScore += score
		}
	}

	denominator := len(origMap)
	if denominator == 0 {
		denominator = len(newMap)
	}
	if denominator == 0 {
		denominator = 1
	}

	return boxEditMetrics{
		ChangedCount: changed,
		AddedCount:   added,
		DeletedCount: deleted,
		ChangeScore:  totalScore / float64(denominator),
	}
}

func boxDeltaScore(a, b bbox, pageW, pageH int) float64 {
	if a == b {
		return 0
	}

	axc := float64(a.x1+a.x2) / 2.0
	ayc := float64(a.y1+a.y2) / 2.0
	bxc := float64(b.x1+b.x2) / 2.0
	byc := float64(b.y1+b.y2) / 2.0

	aw := float64(maxInt(1, a.x2-a.x1))
	ah := float64(maxInt(1, a.y2-a.y1))
	bw := float64(maxInt(1, b.x2-b.x1))
	bh := float64(maxInt(1, b.y2-b.y1))

	dx := absFloat(axc-bxc) / float64(pageW)
	dy := absFloat(ayc-byc) / float64(pageH)
	dw := absFloat(aw-bw) / float64(pageW)
	dh := absFloat(ah-bh) / float64(pageH)

	return (dx + dy + dw + dh) / 4.0
}

func extractPageDimensions(hocrXML string) (int, int) {
	// title may contain other tokens before bbox, e.g. title='image "…"; bbox 0 0 3312 2159'
	re := regexp.MustCompile(`ocr_page[^>]*bbox\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)`)
	matches := re.FindStringSubmatch(hocrXML)
	if len(matches) != 5 {
		return 0, 0
	}
	x2, errX := strconv.Atoi(matches[3])
	y2, errY := strconv.Atoi(matches[4])
	if errX != nil || errY != nil {
		return 0, 0
	}
	return x2, y2
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func startProgress(id, status, message string) {
	now := time.Now()
	progressMu.Lock()
	progressState[id] = processProgress{
		ID:        id,
		Status:    status,
		Message:   message,
		Done:      false,
		StartedAt: now,
		UpdatedAt: now,
	}
	progressMu.Unlock()
}

func updateProgress(id, status, message string) {
	now := time.Now()
	progressMu.Lock()
	state, ok := progressState[id]
	if !ok {
		state = processProgress{ID: id, StartedAt: now}
	}
	if status != "" {
		state.Status = status
	}
	if message != "" {
		state.Message = message
	}
	state.UpdatedAt = now
	progressState[id] = state
	progressMu.Unlock()
}

func finishProgress(id, status, message, errMsg string) {
	now := time.Now()
	progressMu.Lock()
	state, ok := progressState[id]
	if !ok {
		state = processProgress{ID: id, StartedAt: now}
	}
	if status != "" {
		state.Status = status
	}
	if message != "" {
		state.Message = message
	}
	state.Done = true
	state.Error = errMsg
	state.UpdatedAt = now
	progressState[id] = state
	progressMu.Unlock()
}

func startProgressHeartbeat(id string) func() {
	done := make(chan struct{})
	ticker := time.NewTicker(2 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				updateProgress(id, "", "")
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() {
		close(done)
	}
}

func (h *Handler) handleWeb(w http.ResponseWriter, r *http.Request) {
	if h.webDir == "" {
		http.NotFound(w, r)
		return
	}

	relPath := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	target := filepath.Join(h.webDir, relPath)
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		http.ServeFile(w, r, target)
		return
	}

	h.serveIndexHTML(w, r)
}

func detectWebDir() string {
	candidates := []string{
		"/app/web-dist",
		"web/dist",
	}

	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dir
		}
	}

	return ""
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
		scheme = forwardedProto
	}
	host := strings.TrimSpace(r.Host)
	if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = forwardedHost
	}
	return scheme + "://" + host
}

func resolvePublicBase(raw string, r *http.Request, fallbackPath string) string {
	base := strings.TrimSpace(raw)
	if base == "" {
		base = strings.TrimSpace(fallbackPath)
	}
	if base == "" || base == "/" {
		return requestOrigin(r)
	}
	if strings.HasPrefix(base, "http://") || strings.HasPrefix(base, "https://") {
		return strings.TrimRight(base, "/")
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	if base == "/" {
		return requestOrigin(r)
	}
	return requestOrigin(r) + strings.TrimRight(base, "/")
}

func (h *Handler) serveIndexHTML(w http.ResponseWriter, r *http.Request) {
	indexPath := filepath.Join(h.webDir, "index.html")
	content, err := os.ReadFile(indexPath)
	if err != nil {
		http.ServeFile(w, r, indexPath)
		return
	}

	runtimeConfig, err := json.Marshal(map[string]string{
		"ANNOTATION_API_BASE": resolvePublicBase(os.Getenv("ANNOTATION_API_BASE"), r, "/"),
		"CANTALOUPE_IIIF_BASE": resolvePublicBase(os.Getenv("CANTALOUPE_IIIF_BASE"), r, "/cantaloupe/iiif/2"),
	})
	if err != nil {
		http.ServeFile(w, r, indexPath)
		return
	}

	snippet := "<script>window.__SCRIBE_RUNTIME_CONFIG=" + string(runtimeConfig) + ";</script>"
	html := string(content)
	if strings.Contains(html, "</head>") {
		html = strings.Replace(html, "</head>", snippet+"</head>", 1)
	} else {
		html = snippet + html
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, html)
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

// handleExportAnnotations exports OCR annotations for an item image in the
// requested format (hocr, pagexml, alto, or txt).
func (h *Handler) handleExportAnnotations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	idStr := strings.TrimSpace(r.PathValue("item_image_id"))
	itemImageID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid item_image_id")
		return
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "hocr"
	}
	switch format {
	case "hocr", "pagexml", "alto", "txt":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "format must be one of: hocr, pagexml, alto, txt")
		return
	}

	// Fetch or cache the OCR run; returns "item image not found" error when none exists.
	run, err := h.fetchOrCacheHOCRRun(ctx, itemImageID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Determine the canvas URI for the item image.
	img, err := h.items.GetImage(ctx, itemImageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "item image not found")
		return
	}

	// Determine the annotation API base URL.
	base := resolvePublicBase(os.Getenv("ANNOTATION_API_BASE"), r, "/")

	// Build annotation page JSON: prefer saved annotations over hOCR fallback.
	var annotationPageJSON string
	canvasURI := strings.TrimSpace(img.CanvasURI)

	if canvasURI != "" {
		// Try to get annotations from the annotations table (edited state), falling
		// back to hOCR-derived annotations via bootstrapAnnotationsForCanvas.
		items, bootstrapErr := h.bootstrapAnnotationsForCanvas(ctx, canvasURI, base)
		if bootstrapErr == nil {
			page := map[string]any{
				"@context": annotationPageContexts(),
				"id":       annotationPageID(canvasURI),
				"type":     "AnnotationPage",
				"items":    items,
			}
			if b, jsonErr := json.Marshal(page); jsonErr == nil {
				annotationPageJSON = string(b)
			}
		}
	}

	// Fallback: build annotations inline from hOCR (same as handleGetIIIFAnnotations).
	if annotationPageJSON == "" {
		hocrXML := strings.TrimSpace(run.OriginalHOCR)
		if run.CorrectedHOCR != nil && strings.TrimSpace(*run.CorrectedHOCR) != "" {
			hocrXML = strings.TrimSpace(*run.CorrectedHOCR)
		}
		if persisted, ok := readPreferredSessionHOCR(run.SessionID); ok {
			hocrXML = persisted
		}
		if hocrXML == "" {
			writeError(w, http.StatusNotFound, "no annotations available")
			return
		}
		annotationScopeID := run.SessionID
		if run.ItemImageID != nil {
			annotationScopeID = fmt.Sprintf("item-image-%d", *run.ItemImageID)
		}
		manifestBase := fmt.Sprintf("/v1/item-images/%d", itemImageID)
		manifestID := base + manifestBase + "/manifest"
		internalCanvasID := fmt.Sprintf("%s/canvas/page-1", manifestID)
		lines, parseErr := hocr.ParseHOCRLines(hocrXML)
		if parseErr != nil {
			writeError(w, http.StatusInternalServerError, "unable to parse hocr lines")
			return
		}
		annItems := buildLineAnnotations(annotationScopeID, internalCanvasID, lines)
		page := map[string]any{
			"@context": annotationPageContexts(),
			"id":       annotationPageID(internalCanvasID),
			"type":     "AnnotationPage",
			"items":    annItems,
		}
		b, _ := json.Marshal(page)
		annotationPageJSON = string(b)
	}

	// Convert annotation page to the requested format using the crosswalk functions.
	lines, pageW, pageH, err := annotationPageToHOCRLines(annotationPageJSON)
	if err != nil {
		writeError(w, http.StatusNotFound, "no annotations available")
		return
	}

	var content string
	var mimeType string
	var ext string

	switch format {
	case "hocr":
		converter := hocr.NewConverter()
		content = converter.ConvertHOCRLinesToXML(lines, pageW, pageH)
		mimeType = "text/vnd.hocr+html; charset=utf-8"
		ext = "hocr"
	case "pagexml":
		content = linesToPageXML(lines, pageW, pageH)
		mimeType = "application/vnd.prima.page+xml; charset=utf-8"
		ext = "xml"
	case "alto":
		content = linesToALTOXML(lines, pageW, pageH)
		mimeType = "application/alto+xml; charset=utf-8"
		ext = "xml"
	case "txt":
		content = linesToPlainText(lines)
		mimeType = "text/plain; charset=utf-8"
		ext = "txt"
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"item-%d.%s\"", itemImageID, ext))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}

// handleGetContextMetrics returns aggregate OCR metrics for a context.
func (h *Handler) handleGetContextMetrics(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimSpace(r.PathValue("context_id"))
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid context_id")
		return
	}
	ctx := r.Context()
	// Verify context exists.
	c, err := h.contexts.Get(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "context not found")
		return
	}
	metrics, err := h.ocrRuns.GetContextMetrics(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"context": c,
		"metrics": metrics,
	})
}
