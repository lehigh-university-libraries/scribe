package server

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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

	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/vaultkv"
)

type Handler struct {
	ocrRuns            *store.OCRRunStore
	items              *store.ItemStore
	contexts           *store.ContextStore
	annotations        *store.AnnotationStore
	providerCallAudits *store.ProviderCallAuditStore
	providerSecrets    *store.ProviderSecretStore
	transcriptionJobs  *store.TranscriptionJobStore
	auth               *auth.Manager
	vault              *vaultkv.Client
	events             *eventBroker
	webhookClient      *http.Client
	webhookURLs        []string
	mux                http.Handler
	webDir             string
	ocr                *ocrhandlers.Handler
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
	progressMu       sync.RWMutex
	progressState    = map[string]processProgress{}
	trustedProxyNets = mustParseCIDRs(
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10",
		"169.254.0.0/16",
		"fc00::/7",
	)
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("invalid trusted proxy CIDR %q: %v", cidr, err))
		}
		nets = append(nets, network)
	}
	return nets
}

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
	authManager *auth.Manager,
	providerSecrets *store.ProviderSecretStore,
	vaultClient *vaultkv.Client,
	auditStores ...*store.ProviderCallAuditStore,
) *Handler {
	webDir := detectWebDir()
	if webDir == "" {
		slog.Info("web assets directory not found; running in API-only mode")
	} else {
		slog.Info("serving web assets", "dir", webDir)
	}

	var providerCallAudits *store.ProviderCallAuditStore
	if len(auditStores) > 0 {
		providerCallAudits = auditStores[0]
	}

	handler := &Handler{
		ocrRuns:            ocrRuns,
		items:              items,
		contexts:           contexts,
		annotations:        annotations,
		providerCallAudits: providerCallAudits,
		providerSecrets:    providerSecrets,
		transcriptionJobs:  transcriptionJobs,
		auth:               authManager,
		vault:              vaultClient,
		events:             newEventBroker(),
		webhookClient:      &http.Client{Timeout: 10 * time.Second},
		webhookURLs:        append([]string(nil), config.Get().Config.Webhooks.URLs...),
		webDir:             webDir,
		ocr:                ocrhandlers.New(),
	}
	if providerCallAudits != nil {
		handler.ocr.SetProviderCallAuditLogger(func(ctx context.Context, record hocr.ProviderCallAuditRecord) {
			if err := providerCallAudits.Create(ctx, store.ProviderCallAudit{
				SessionID:    record.SessionID,
				ItemImageID:  record.ItemImageID,
				ContextID:    record.ContextID,
				Provider:     record.Provider,
				Model:        record.Model,
				Operation:    record.Operation,
				Prompt:       record.Prompt,
				RequestJSON:  record.RequestJSON,
				ResponseJSON: record.ResponseJSON,
				ErrorMessage: record.ErrorMessage,
				HTTPStatus:   record.HTTPStatus,
			}); err != nil {
				slog.Warn("failed to persist provider call audit", "error", err, "provider", record.Provider, "model", record.Model, "operation", record.Operation)
			}
		})
	}
	mux := http.NewServeMux()
	registerConnectServices(mux, handler, authManager, connectHandlerOptions(authManager)...)

	// Health
	mux.HandleFunc("GET /healthz", handler.handleHealth)

	// IIIF presentation endpoints used by the editor.
	mux.HandleFunc("GET /v1/items/{item_id}/manifest", handler.handleGetItemIIIFManifest)
	mux.HandleFunc("GET /v1/items/{item_id}/export", handler.handleExportItem)
	mux.HandleFunc("GET /v1/items/{item_id}/provider-call-audits", handler.handleListItemProviderCallAudits)
	mux.HandleFunc("GET /v1/item-images/{item_image_id}/manifest", handler.handleGetIIIFManifest)
	mux.HandleFunc("GET /v1/item-images/{item_image_id}/annotations", handler.handleGetIIIFAnnotations)
	mux.HandleFunc("GET /v1/item-images/{item_image_id}/hocr", handler.handleGetHOCR)
	mux.HandleFunc("GET /v1/item-images/{item_image_id}/export", handler.handleExportAnnotations)
	mux.HandleFunc("GET /v1/events", handler.handleEventStream)
	mux.HandleFunc("POST /scribe.v1.AnnotationService/PublishItemImageEdits", handler.handlePublishItemImageEdits)

	// Context metrics
	mux.HandleFunc("GET /v1/contexts/{context_id}/metrics", handler.handleGetContextMetrics)

	if authManager != nil {
		authManager.RegisterRoutes(mux)
	}

	// Static assets
	mux.Handle("GET /static/uploads/", http.StripPrefix("/static/uploads/", http.FileServer(http.Dir("uploads"))))
	mux.HandleFunc("/", handler.handleWeb)
	var finalMux http.Handler = mux
	if authManager != nil {
		finalMux = authManager.Middleware(finalMux)
	}
	handler.mux = AccessLogger(finalMux)
	return handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS: allow all origins for annotation / Connect RPC clients.
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = "*"
	} else {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Accept,Authorization,Connect-Protocol-Version,X-Provider,X-Scribe-Workspace-ID,X-Scribe-API-Key")
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
		if isNotFoundError(err) || strings.Contains(strings.ToLower(err.Error()), "not found") {
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

func (h *Handler) handleListItemProviderCallAudits(w http.ResponseWriter, r *http.Request) {
	itemID := strings.TrimSpace(r.PathValue("item_id"))
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "item_id is required")
		return
	}
	if _, err := h.itemForRequest(r.Context(), itemID); err != nil {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	if h.providerCallAudits == nil {
		writeJSON(w, http.StatusOK, map[string]any{"itemId": itemID, "audits": []any{}})
		return
	}

	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 500 {
			limit = v
		}
	}
	audits, err := h.providerCallAudits.ListByItem(r.Context(), itemID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load provider call audits")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"itemId": itemID,
		"audits": audits,
	})
}

func (h *Handler) handleGetItemIIIFManifest(w http.ResponseWriter, r *http.Request) {
	itemID := strings.TrimSpace(r.PathValue("item_id"))
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "item_id is required")
		return
	}
	item, err := h.itemForRequest(r.Context(), itemID)
	if err != nil {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	if len(item.Images) == 0 {
		writeError(w, http.StatusNotFound, "item has no images")
		return
	}

	apiBase := requestOrigin(r)
	manifestID := fmt.Sprintf("%s/v1/items/%s/manifest", apiBase, url.PathEscape(item.ID))
	iiifBase := resolvePublicBase(config.Get().Config.IIIF.Base, r, "/iiif/3")
	canvases := make([]any, 0, len(item.Images))

	for _, image := range item.Images {
		canvasID := strings.TrimSpace(image.CanvasURI)
		if canvasID == "" {
			canvasID = fmt.Sprintf("%s/v1/item-images/%d/manifest/canvas/page-1", apiBase, image.ID)
			if err := h.items.UpdateImageCanvasURI(r.Context(), image.ID, canvasID); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to persist canvas uri")
				return
			}
		}

		pageW, pageH := 1, 1
		seeAlso := make([]any, 0, 1)
		if run, err := h.fetchOrCacheHOCRRun(r.Context(), image.ID); err == nil {
			hocrXML := strings.TrimSpace(run.OriginalHOCR)
			if run.CorrectedHOCR != nil && strings.TrimSpace(*run.CorrectedHOCR) != "" {
				hocrXML = strings.TrimSpace(*run.CorrectedHOCR)
			}
			if persisted, ok := readPreferredSessionHOCR(run.SessionID); ok {
				hocrXML = persisted
			}
			pageW, pageH = extractPageDimensions(hocrXML)
			if pageW <= 0 {
				pageW = 1
			}
			if pageH <= 0 {
				pageH = 1
			}
			seeAlso = append(seeAlso, map[string]any{
				"id":      fmt.Sprintf("%s/v1/item-images/%d/hocr", apiBase, image.ID),
				"type":    "Text",
				"format":  "text/vnd.hocr+html",
				"profile": "http://kba.cloud/hocr-spec",
				"label":   map[string]any{"none": []string{"hOCR embedded text"}},
			})
		}

		canvasLabel := strings.TrimSpace(image.Label)
		if canvasLabel == "" {
			canvasLabel = image.ImageURL
			if iiifID, err := iiifIdentifierFromImageURL(image.ImageURL); err == nil {
				canvasLabel = iiifID
			}
		}

		paintingPageID := fmt.Sprintf("%s/page/painting", canvasID)
		paintingAnnID := fmt.Sprintf("%s/annotation/painting", canvasID)
		canvas := map[string]any{
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
							"body":       buildImageBody(image.ImageURL, iiifBase, pageW, pageH),
						},
					},
				},
			},
			"annotations": []any{
				map[string]any{
					"id":   fmt.Sprintf("%s/v1/item-images/%d/annotations", apiBase, image.ID),
					"type": "AnnotationPage",
				},
			},
		}
		if len(seeAlso) > 0 {
			canvas["seeAlso"] = seeAlso
		}
		canvases = append(canvases, canvas)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"@context": "http://iiif.io/api/presentation/3/context.json",
		"id":       manifestID,
		"type":     "Manifest",
		"label":    map[string]any{"none": []string{item.Name}},
		"items":    canvases,
	})
}

func sanitizeFilenamePart(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return fallback
	}
	return out
}

func (h *Handler) handleGetIIIFManifest(w http.ResponseWriter, r *http.Request) {
	itemImageID, err := itemImageIDFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	run, manifestPath, annotationsPath, hocrPath, err := h.resolveRunAndIIIFPaths(r)
	if err != nil {
		if isNotFoundError(err) || strings.Contains(strings.ToLower(err.Error()), "not found") {
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

	img, err := h.itemImageForRequest(r.Context(), itemImageID)
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

	iiifBase := resolvePublicBase(config.Get().Config.IIIF.Base, r, "/iiif/3")
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
	itemImageID, err := itemImageIDFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	run, manifestPath, annotationsPath, _, err := h.resolveRunAndIIIFPaths(r)
	if err != nil {
		if isNotFoundError(err) || strings.Contains(strings.ToLower(err.Error()), "not found") {
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

	apiBase := requestOrigin(r)
	manifestID := apiBase + manifestPath
	canvasID := fmt.Sprintf("%s/canvas/page-1", manifestID)
	pageID := fmt.Sprintf("%s%s?textGranularity=%s", apiBase, annotationsPath, granularity)
	annotationScopeID := run.SessionID
	if run.ItemImageID != nil {
		annotationScopeID = fmt.Sprintf("item-image-%d", *run.ItemImageID)
	}

	img, err := h.itemImageForRequest(r.Context(), itemImageID)
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

func lineAnnotationText(line models.HOCRLine) string {
	return joinLineWords(line)
}

func buildLineAnnotations(sessionID, canvasID string, lines []models.HOCRLine) []any {
	items := make([]any, 0, len(lines))
	for i, line := range lines {
		width := line.BBox.X2 - line.BBox.X1
		height := line.BBox.Y2 - line.BBox.Y1
		if width <= 0 || height <= 0 {
			continue
		}
		text := strings.TrimSpace(lineAnnotationText(line))
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
	if !errors.Is(err, sql.ErrNoRows) {
		return store.OCRRun{}, err
	}
	// No OCR run yet — try to fetch and cache hOCR from the item_image's hocr_url.
	img, imgErr := h.items.GetImage(ctx, itemImageID)
	if imgErr != nil {
		if errors.Is(imgErr, sql.ErrNoRows) {
			return store.OCRRun{}, sql.ErrNoRows
		}
		return store.OCRRun{}, imgErr
	}
	if img.HocrURL == "" {
		return store.OCRRun{}, sql.ErrNoRows
	}
	hocrXML, fetchErr := fetchHOCRContent(ctx, img.HocrURL)
	if fetchErr != nil || strings.TrimSpace(hocrXML) == "" {
		slog.Warn("on-demand hOCR fetch failed", "item_image_id", itemImageID, "hocr_url", img.HocrURL, "error", fetchErr)
		return store.OCRRun{}, sql.ErrNoRows
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
	if _, err := h.itemImageForRequest(ctx, itemImageID); err != nil {
		return store.OCRRun{}, "", "", "", fmt.Errorf("item image not found")
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
		base = strings.TrimRight(strings.TrimSpace(config.Get().Config.Annotation.APIInternalBase), "/")
	}
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(config.Get().Config.Annotation.APIBase), "/")
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
	items := buildLineAnnotations(annotationScopeID, canvasURI, lines)
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

func fetchIIIFRegionToTemp(iiifID string, x1, y1, x2, y2 int) (string, func(), error) {
	width := x2 - x1
	height := y2 - y1
	if width <= 0 || height <= 0 {
		return "", func() {}, fmt.Errorf("invalid bbox")
	}
	base := strings.TrimRight(strings.TrimSpace(config.Get().Config.IIIF.InternalBase), "/")
	if base == "" {
		return "", func() {}, fmt.Errorf("iiif.internal_base is not configured")
	}
	cropURL := fmt.Sprintf("%s/%s/%d,%d,%d,%d/max/0/default.jpg", base, iiifID, x1, y1, width, height)
	return fetchImageURLToTemp(cropURL, "scribe-region-*.jpg")
}

func (h *Handler) startAsyncTranscription(sessionID, imageURL, provider, model string, workspaceID uint64, userID *uint64) {
	go func() {
		ctx := context.Background()
		ctx = h.contextWithProviderSecret(ctx, workspaceID, userID, provider)
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

				regionPath, cleanup, err := fetchImageRegionToTemp(imageURL, outLine.BBox.X1, outLine.BBox.Y1, outLine.BBox.X2, outLine.BBox.Y2)
				if err != nil {
					slog.Warn("Async line fetch failed", "session_id", sessionID, "line_id", outLine.ID, "error", err)
					outLine.Words = nil
					results <- lineResult{idx: job.idx, line: outLine}
					continue
				}
				text, err := h.ocr.TranscribeImageFileWithContext(ctx, regionPath, provider, model)
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
	if v := config.Get().Config.LLM.LineTranscribeConcurrency; v > 0 {
		return v
	}
	return 5
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

	// Local upload: use the standalone IIIF image service.
	if strings.HasPrefix(imageURL, "/static/uploads/") {
		iiifID, err := iiifIdentifierFromImageURL(imageURL)
		if err == nil {
			serviceID := iiifBase + "/" + iiifID
			body["id"] = serviceID + "/full/max/0/default.jpg"
			body["format"] = "image/jpeg"
			body["service"] = []any{iiifServiceDescriptor(serviceID)}
			return body
		}
	}

	// External URL: use as-is and try to attach a IIIF service descriptor.
	body["id"] = imageURL
	body["format"] = "image/jpeg"
	if serviceID := iiifServiceFromImageURL(imageURL); serviceID != "" {
		body["service"] = []any{iiifServiceDescriptor(serviceID)}
	}
	return body
}

func iiifServiceDescriptor(serviceID string) map[string]any {
	if strings.Contains(serviceID, "/iiif/3/") {
		return map[string]any{
			"id":      serviceID,
			"type":    "ImageService3",
			"profile": "level2",
		}
	}
	return map[string]any{
		"id":      serviceID,
		"type":    "ImageService2",
		"profile": "http://iiif.io/api/image/2/level2.json",
	}
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

	cfg := config.Get().Config.LLM
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	}
	if provider == "" {
		provider = "ollama"
	}
	switch provider {
	case "kraken":
		if cfg.Kraken.Model != "" {
			return cfg.Kraken.Model
		}
		return "catmus-print-fondue-large.mlmodel"
	case "openai":
		if cfg.OpenAI.Model != "" {
			return cfg.OpenAI.Model
		}
		return "gpt-4o"
	case "gemini":
		if cfg.Gemini.Model != "" {
			return cfg.Gemini.Model
		}
		return "gemini-2.0-flash"
	default:
		if cfg.Ollama.Model != "" {
			return cfg.Ollama.Model
		}
		return "glm-ocr:bf16"
	}
}

func effectiveProvider(requestProvider string) string {
	p := strings.ToLower(strings.TrimSpace(requestProvider))
	if p != "" {
		return p
	}
	if cfg := strings.ToLower(strings.TrimSpace(config.Get().Config.LLM.Provider)); cfg != "" {
		return cfg
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

func isTrustedProxy(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, network := range trustedProxyNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func firstForwardedValue(raw string) string {
	if raw == "" {
		return ""
	}
	part := strings.TrimSpace(strings.Split(raw, ",")[0])
	part = strings.Trim(part, "\"")
	return strings.TrimSpace(part)
}

func forwardedParams(raw string) map[string]string {
	params := map[string]string{}
	entry := firstForwardedValue(raw)
	if entry == "" {
		return params
	}
	for _, part := range strings.Split(entry, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(strings.Trim(value, "\""))
		if key != "" && value != "" {
			params[key] = value
		}
	}
	return params
}

func normalizeOriginHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if parsedHost, parsedPort, err := net.SplitHostPort(host); err == nil {
		if parsedHost != "" {
			return parsedHost
		}
		if parsedPort != "" {
			return host
		}
	}
	if strings.Count(host, ":") == 1 {
		if maybeHost, _, ok := strings.Cut(host, ":"); ok && maybeHost != "" {
			return maybeHost
		}
	}
	return host
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if !isTrustedProxy(r.RemoteAddr) {
		return scheme + "://" + normalizeOriginHost(r.Host)
	}

	forwarded := forwardedParams(r.Header.Get("Forwarded"))
	if forwardedProto := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
		scheme = forwardedProto
	}
	if forwarded["proto"] != "" {
		scheme = forwarded["proto"]
	}

	host := firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" && forwarded["host"] != "" {
		host = forwarded["host"]
	}
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}

	return scheme + "://" + normalizeOriginHost(host)
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
		"ANNOTATION_API_BASE": resolvePublicBase(config.Get().Config.Annotation.APIBase, r, "/"),
		"IIIF_BASE":           resolvePublicBase(config.Get().Config.IIIF.Base, r, "/iiif/3"),
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

func (h *Handler) exportItemImageContent(ctx context.Context, itemImageID uint64, format string, base string) (string, string, string, error) {
	run, err := h.fetchOrCacheHOCRRun(ctx, itemImageID)
	if err != nil {
		return "", "", "", err
	}
	img, err := h.items.GetImage(ctx, itemImageID)
	if err != nil {
		return "", "", "", fmt.Errorf("item image not found")
	}

	var annotationPageJSON string
	annotationSource := "none"
	canvasURI := strings.TrimSpace(img.CanvasURI)

	if canvasURI != "" {
		dbPayloads, dbErr := h.annotations.SearchByCanvas(ctx, canvasURI)
		slog.Info("Export annotations DB lookup",
			"item_image_id", itemImageID,
			"canvas_uri", canvasURI,
			"payload_count", len(dbPayloads),
			"db_error", dbErr,
		)
		if dbErr == nil && len(dbPayloads) > 0 {
			dbItems := make([]any, 0, len(dbPayloads))
			for i, p := range dbPayloads {
				var anno map[string]any
				if json.Unmarshal([]byte(p), &anno) == nil {
					dbItems = append(dbItems, anno)
					if i < 3 {
						slog.Info("Export annotations DB payload sample",
							"item_image_id", itemImageID,
							"index", i,
							"annotation", annotationDebugSummary(anno),
						)
					}
				}
			}
			if len(dbItems) > 0 {
				page := map[string]any{
					"@context": annotationPageContexts(),
					"id":       annotationPageID(canvasURI),
					"type":     "AnnotationPage",
					"items":    dbItems,
				}
				if b, jsonErr := json.Marshal(page); jsonErr == nil {
					annotationPageJSON = string(b)
					annotationSource = "db"
				}
			}
		}
		if annotationPageJSON == "" {
			items, bootstrapErr := h.bootstrapAnnotationsForCanvas(ctx, canvasURI, base)
			slog.Info("Export annotations bootstrap fallback",
				"item_image_id", itemImageID,
				"canvas_uri", canvasURI,
				"item_count", len(items),
				"bootstrap_error", bootstrapErr,
			)
			if bootstrapErr == nil {
				page := map[string]any{
					"@context": annotationPageContexts(),
					"id":       annotationPageID(canvasURI),
					"type":     "AnnotationPage",
					"items":    items,
				}
				if b, jsonErr := json.Marshal(page); jsonErr == nil {
					annotationPageJSON = string(b)
					annotationSource = "bootstrap"
				}
			}
		}
	}

	if annotationPageJSON == "" {
		hocrXML := strings.TrimSpace(run.OriginalHOCR)
		if run.CorrectedHOCR != nil && strings.TrimSpace(*run.CorrectedHOCR) != "" {
			hocrXML = strings.TrimSpace(*run.CorrectedHOCR)
		}
		if persisted, ok := readPreferredSessionHOCR(run.SessionID); ok {
			hocrXML = persisted
		}
		if hocrXML == "" {
			return "", "", "", fmt.Errorf("no annotations available")
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
			return "", "", "", fmt.Errorf("unable to parse hocr lines")
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
		annotationSource = "hocr-fallback"
	}

	lines, pageW, pageH, err := annotationPageToHOCRLines(annotationPageJSON)
	if err != nil {
		slog.Warn("Export annotations crosswalk failed",
			"item_image_id", itemImageID,
			"format", format,
			"canvas_uri", canvasURI,
			"source", annotationSource,
			"error", err,
		)
		return "", "", "", fmt.Errorf("no annotations available")
	}
	slog.Info("Export annotations crosswalk succeeded",
		"item_image_id", itemImageID,
		"format", format,
		"canvas_uri", canvasURI,
		"source", annotationSource,
		"line_count", len(lines),
		"page_w", pageW,
		"page_h", pageH,
	)

	switch format {
	case "hocr":
		converter := hocr.NewConverter()
		return converter.ConvertHOCRLinesToXML(lines, pageW, pageH), "text/vnd.hocr+html; charset=utf-8", "hocr", nil
	case "pagexml":
		return linesToPageXML(lines, pageW, pageH), "application/vnd.prima.page+xml; charset=utf-8", "xml", nil
	case "alto":
		return linesToALTOXML(lines, pageW, pageH), "application/alto+xml; charset=utf-8", "xml", nil
	case "txt":
		return linesToPlainText(lines), "text/plain; charset=utf-8", "txt", nil
	default:
		return "", "", "", fmt.Errorf("format must be one of: hocr, pagexml, alto, txt")
	}
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
	if _, err := h.itemImageForRequest(ctx, itemImageID); err != nil {
		writeError(w, http.StatusNotFound, "item image not found")
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "hocr"
	}
	base := resolvePublicBase(config.Get().Config.Annotation.APIBase, r, "/")
	content, mimeType, ext, err := h.exportItemImageContent(ctx, itemImageID, format, base)
	if err != nil {
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "not found") || strings.Contains(strings.ToLower(msg), "no annotations") {
			writeError(w, http.StatusNotFound, msg)
			return
		}
		if strings.Contains(strings.ToLower(msg), "format must") {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		writeError(w, http.StatusInternalServerError, msg)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"item-%d.%s\"", itemImageID, ext))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}

func (h *Handler) handleExportItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	itemID := strings.TrimSpace(r.PathValue("item_id"))
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "item_id is required")
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "hocr"
	}
	switch format {
	case "hocr", "pagexml", "alto", "txt":
	default:
		writeError(w, http.StatusBadRequest, "format must be one of: hocr, pagexml, alto, txt")
		return
	}
	item, err := h.itemForRequest(ctx, itemID)
	if err != nil {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	if len(item.Images) == 0 {
		writeError(w, http.StatusNotFound, "item has no images")
		return
	}
	base := resolvePublicBase(config.Get().Config.Annotation.APIBase, r, "/")
	safeName := sanitizeFilenamePart(item.Name, "item-"+item.ID)

	if len(item.Images) == 1 {
		content, mimeType, ext, err := h.exportItemImageContent(ctx, item.Images[0].ID, format, base)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.%s\"", safeName, ext))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
		return
	}

	if format == "txt" {
		pages := make([]string, 0, len(item.Images))
		for _, img := range item.Images {
			content, _, _, err := h.exportItemImageContent(ctx, img.ID, format, base)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			pages = append(pages, strings.TrimSpace(content))
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.txt\"", safeName))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Join(pages, "\n\n")))
		return
	}

	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for _, img := range item.Images {
		content, _, ext, err := h.exportItemImageContent(ctx, img.ID, format, base)
		if err != nil {
			_ = archive.Close()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		entryName := fmt.Sprintf("%s-page-%04d.%s", safeName, img.Sequence, ext)
		if img.Sequence == 0 {
			entryName = fmt.Sprintf("%s-image-%d.%s", safeName, img.ID, ext)
		}
		f, err := archive.Create(entryName)
		if err != nil {
			_ = archive.Close()
			writeError(w, http.StatusInternalServerError, "failed to create export archive")
			return
		}
		if _, err := f.Write([]byte(content)); err != nil {
			_ = archive.Close()
			writeError(w, http.StatusInternalServerError, "failed to write export archive")
			return
		}
	}
	if err := archive.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finalize export archive")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-%s.zip\"", safeName, format))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
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
	c, err := h.contextForRead(ctx, id)
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
