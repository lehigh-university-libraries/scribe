package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	legacyhandlers "github.com/lehigh-university-libraries/hOCRedit/internal/handlers"
	"github.com/lehigh-university-libraries/hOCRedit/internal/metrics"
	"github.com/lehigh-university-libraries/hOCRedit/internal/store"
	"github.com/lehigh-university-libraries/hOCRedit/proto/hocredit/v1/hocreditv1connect"
)

type Handler struct {
	sessions *store.SessionStore
	ocrRuns  *store.OCRRunStore
	mux      *http.ServeMux
	webDir   string
	legacy   *legacyhandlers.Handler
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

func NewHandler(sessions *store.SessionStore, ocrRuns *store.OCRRunStore) *Handler {
	webDir := detectWebDir()
	if webDir == "" {
		slog.Warn("web assets directory not found; root path will return 404")
	} else {
		slog.Info("serving web assets", "dir", webDir)
	}

	handler := &Handler{
		sessions: sessions,
		ocrRuns:  ocrRuns,
		webDir:   webDir,
		legacy:   legacyhandlers.New(),
	}
	mux := http.NewServeMux()
	imageAPIPath, imageAPIHandler := hocreditv1connect.NewImageProcessingServiceHandler(handler)
	mux.Handle(imageAPIPath, imageAPIHandler)
	sessionAPIPath, sessionAPIHandler := hocreditv1connect.NewSessionServiceHandler(handler)
	mux.Handle(sessionAPIPath, sessionAPIHandler)
	mux.HandleFunc("GET /healthz", handler.handleHealth)
	mux.HandleFunc("GET /v1/sessions", handler.handleListSessions)
	mux.HandleFunc("POST /v1/sessions", handler.handleCreateSession)
	mux.HandleFunc("POST /v1/process/url", handler.handleProcessURL)
	mux.HandleFunc("POST /v1/process/upload", handler.handleProcessUpload)
	mux.HandleFunc("POST /v1/process/hocr", handler.handleProcessHOCR)
	mux.HandleFunc("GET /v1/progress/{progress_id}", handler.handleGetProgress)
	mux.HandleFunc("GET /v1/llm/options", handler.handleLLMOptions)
	mux.HandleFunc("GET /v1/ocr/runs/{session_id}", handler.handleGetOCRRun)
	mux.HandleFunc("PUT /v1/ocr/runs/{session_id}/edits", handler.handleSaveOCREdits)
	mux.Handle("GET /static/uploads/", http.StripPrefix("/static/uploads/", http.FileServer(http.Dir("uploads"))))
	mux.HandleFunc("/", handler.handleWeb)
	handler.mux = mux
	return handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.sessions.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h *Handler) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if req.ID == "" {
		req.ID = time.Now().UTC().Format("20060102150405")
	}
	if req.Name == "" {
		req.Name = "Untitled Session"
	}

	session, err := h.sessions.Create(r.Context(), req.ID, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, session)
}

func (h *Handler) handleProcessURL(w http.ResponseWriter, r *http.Request) {
	progressID := strings.TrimSpace(r.Header.Get("X-Progress-ID"))
	if progressID != "" {
		w.Header().Set("X-Progress-ID", progressID)
		startProgress(progressID, "starting", "Validating request")
		defer startProgressHeartbeat(progressID)()
	}

	var req struct {
		ImageURL string `json:"image_url"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "Invalid JSON", "invalid json")
		}
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.ImageURL) == "" {
		if progressID != "" {
			finishProgress(progressID, "failed", "image_url is required", "image_url is required")
		}
		writeError(w, http.StatusBadRequest, "image_url is required")
		return
	}
	if progressID != "" {
		updateProgress(progressID, "processing", "Running OCR")
	}

	result, err := h.legacy.ProcessImageURLWithProviderAndModel(req.ImageURL, req.Provider, req.Model)
	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "OCR processing failed", err.Error())
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if progressID != "" {
		updateProgress(progressID, "saving", "Saving OCR run")
	}
	if err := h.ocrRuns.Create(r.Context(), store.OCRRun{
		SessionID:    result.SessionID,
		ImageURL:     result.ImageURL,
		Provider:     effectiveProvider(req.Provider),
		Model:        effectiveModel(req.Provider, req.Model),
		OriginalHOCR: result.HOCR,
		OriginalText: result.PlainText,
	}); err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "Failed to save OCR run", err.Error())
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}

	h.renderProcessedOutput(w, r, result)
}

func (h *Handler) handleProcessUpload(w http.ResponseWriter, r *http.Request) {
	progressID := strings.TrimSpace(r.Header.Get("X-Progress-ID"))
	if progressID != "" {
		w.Header().Set("X-Progress-ID", progressID)
		startProgress(progressID, "starting", "Reading upload")
		defer startProgressHeartbeat(progressID)()
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "Invalid multipart form", "invalid multipart form")
		}
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	file, fileHeader, err := extractUploadFile(r)
	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "Missing upload file", err.Error())
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer file.Close()

	fileData, err := io.ReadAll(file)
	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "Failed to read upload", "failed to read upload")
		}
		writeError(w, http.StatusBadRequest, "failed to read upload")
		return
	}
	if progressID != "" {
		updateProgress(progressID, "processing", "Running OCR")
	}

	model := r.FormValue("model")
	provider := r.FormValue("provider")
	result, err := h.legacy.ProcessImageUploadWithProviderAndModel(fileHeader.Filename, fileData, provider, model)
	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "OCR processing failed", err.Error())
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if progressID != "" {
		updateProgress(progressID, "saving", "Saving OCR run")
	}
	if err := h.ocrRuns.Create(r.Context(), store.OCRRun{
		SessionID:    result.SessionID,
		ImageURL:     result.ImageURL,
		Provider:     effectiveProvider(provider),
		Model:        effectiveModel(provider, model),
		OriginalHOCR: result.HOCR,
		OriginalText: result.PlainText,
	}); err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "Failed to save OCR run", err.Error())
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}

	h.renderProcessedOutput(w, r, result)
}

func (h *Handler) handleProcessHOCR(w http.ResponseWriter, r *http.Request) {
	progressID := strings.TrimSpace(r.Header.Get("X-Progress-ID"))
	if progressID != "" {
		w.Header().Set("X-Progress-ID", progressID)
		startProgress(progressID, "starting", "Processing supplied hOCR")
		defer startProgressHeartbeat(progressID)()
	}

	var req struct {
		HOCR     string `json:"hocr"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		ImageURL string `json:"image_url"`
	}

	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			if progressID != "" {
				finishProgress(progressID, "failed", "Invalid multipart form", "invalid multipart form")
			}
			writeError(w, http.StatusBadRequest, "invalid multipart form")
			return
		}
		req.HOCR = r.FormValue("hocr")
		req.Model = r.FormValue("model")
		req.Provider = r.FormValue("provider")
		req.ImageURL = r.FormValue("image_url")

		if file, fileHeader, err := extractUploadFile(r); err == nil {
			defer file.Close()
			fileData, readErr := io.ReadAll(file)
			if readErr != nil {
				if progressID != "" {
					finishProgress(progressID, "failed", "Failed to read uploaded image", "failed to read uploaded image")
				}
				writeError(w, http.StatusBadRequest, "failed to read uploaded image")
				return
			}
			imageURL, storeErr := h.legacy.StoreUploadedImage(fileHeader.Filename, fileData)
			if storeErr != nil {
				if progressID != "" {
					finishProgress(progressID, "failed", "Failed to store uploaded image", storeErr.Error())
				}
				writeError(w, http.StatusInternalServerError, storeErr.Error())
				return
			}
			req.ImageURL = imageURL
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			if progressID != "" {
				finishProgress(progressID, "failed", "Invalid JSON", "invalid json")
			}
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}

	if strings.TrimSpace(req.HOCR) == "" {
		if progressID != "" {
			finishProgress(progressID, "failed", "hocr is required", "hocr is required")
		}
		writeError(w, http.StatusBadRequest, "hocr is required")
		return
	}

	plainText, err := legacyhandlers.HOCRToPlainText(req.HOCR)
	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "invalid hocr", "invalid hocr")
		}
		writeError(w, http.StatusBadRequest, "invalid hocr")
		return
	}
	if progressID != "" {
		updateProgress(progressID, "saving", "Saving OCR run")
	}

	sessionID := fmt.Sprintf("hocr_%d", time.Now().UnixNano())
	run := store.OCRRun{
		SessionID:    sessionID,
		ImageURL:     strings.TrimSpace(req.ImageURL),
		Provider:     "custom",
		Model:        "custom",
		OriginalHOCR: req.HOCR,
		OriginalText: plainText,
	}
	if err := h.ocrRuns.Create(r.Context(), run); err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "Failed to save OCR run", err.Error())
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}

	h.renderProcessedOutput(w, r, &legacyhandlers.ProcessResult{
		SessionID: sessionID,
		HOCR:      req.HOCR,
		PlainText: plainText,
		ImageURL:  run.ImageURL,
	})
}

func (h *Handler) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	progressID := strings.TrimSpace(r.PathValue("progress_id"))
	if progressID == "" {
		writeError(w, http.StatusBadRequest, "progress_id is required")
		return
	}

	progressMu.RLock()
	state, ok := progressState[progressID]
	progressMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "progress not found")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (h *Handler) handleGetOCRRun(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	run, err := h.ocrRuns.Get(r.Context(), sessionID)
	if err != nil {
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "no rows") {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) handleSaveOCREdits(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")

	var req struct {
		CorrectedHOCR string `json:"corrected_hocr"`
		EditCount     int    `json:"edit_count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.CorrectedHOCR) == "" {
		writeError(w, http.StatusBadRequest, "corrected_hocr is required")
		return
	}

	run, err := h.ocrRuns.Get(r.Context(), sessionID)
	if err != nil {
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "no rows") {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	correctedText, err := legacyhandlers.HOCRToPlainText(req.CorrectedHOCR)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid corrected_hocr")
		return
	}

	lev := metrics.LevenshteinDistance(run.OriginalText, correctedText)
	boxMetrics := calculateBoxEditMetrics(run.OriginalHOCR, req.CorrectedHOCR)
	if err := h.ocrRuns.SaveEdits(
		r.Context(),
		sessionID,
		req.CorrectedHOCR,
		correctedText,
		req.EditCount,
		lev,
		boxMetrics.ChangedCount,
		boxMetrics.AddedCount,
		boxMetrics.DeletedCount,
		boxMetrics.ChangeScore,
	); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":            sessionID,
		"edit_count":            req.EditCount,
		"levenshtein_distance":  lev,
		"box_edit_count":        boxMetrics.ChangedCount,
		"boxes_added":           boxMetrics.AddedCount,
		"boxes_deleted":         boxMetrics.DeletedCount,
		"box_change_score":      boxMetrics.ChangeScore,
		"corrected_plain_text":  correctedText,
		"original_plain_text":   run.OriginalText,
		"provider":              run.Provider,
		"model":                 run.Model,
	})
}

func extractUploadFile(r *http.Request) (multipart.File, *multipart.FileHeader, error) {
	file, header, err := r.FormFile("file")
	if err == nil {
		return file, header, nil
	}

	file, header, err = r.FormFile("files")
	if err == nil {
		return file, header, nil
	}

	return nil, nil, err
}

func (h *Handler) renderProcessedOutput(w http.ResponseWriter, r *http.Request, result *legacyhandlers.ProcessResult) {
	format := getOutputFormat(r)
	w.Header().Set("X-Session-ID", result.SessionID)
	w.Header().Set("X-Image-URL", result.ImageURL)

	switch format {
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(result.PlainText))
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(result.HOCR))
	}
}

func getOutputFormat(r *http.Request) string {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "text", "plain":
		return "text"
	case "hocr", "html":
		return "hocr"
	}

	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "text/plain") {
		return "text"
	}

	return "hocr"
}

func handleList(raw string, fallback string) []string {
	values := strings.Split(raw, ",")
	out := make([]string, 0, len(values)+1)
	seen := map[string]bool{}
	for _, item := range values {
		v := strings.TrimSpace(item)
		if v == "" || seen[v] {
			continue
		}
		out = append(out, v)
		seen[v] = true
	}
	if fallback != "" && !seen[fallback] {
		out = append([]string{fallback}, out...)
	}
	return out
}

func (h *Handler) handleLLMOptions(w http.ResponseWriter, _ *http.Request) {
	defaultProvider := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	if defaultProvider == "" {
		defaultProvider = "ollama"
	}

	ollamaDefault := strings.TrimSpace(os.Getenv("OLLAMA_MODEL"))
	if ollamaDefault == "" {
		ollamaDefault = "mistral-small3.2:24b"
	}
	openAIDefault := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if openAIDefault == "" {
		openAIDefault = "gpt-4o"
	}
	geminiDefault := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	if geminiDefault == "" {
		geminiDefault = "gemini-2.0-flash"
	}

	ollamaModels := handleList(os.Getenv("OLLAMA_MODELS"), ollamaDefault)
	openAIModels := handleList(os.Getenv("OPENAI_MODELS"), openAIDefault)
	geminiModels := handleList(os.Getenv("GEMINI_MODELS"), geminiDefault)

	writeJSON(w, http.StatusOK, map[string]any{
		"default_provider": defaultProvider,
		"providers": []map[string]any{
			{
				"id":            "ollama",
				"name":          "Ollama",
				"enabled":       true,
				"default_model": ollamaDefault,
				"models":        ollamaModels,
			},
			{
				"id":            "openai",
				"name":          "OpenAI",
				"enabled":       strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != "",
				"default_model": openAIDefault,
				"models":        openAIModels,
			},
			{
				"id":            "gemini",
				"name":          "Gemini",
				"enabled":       strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) != "",
				"default_model": geminiDefault,
				"models":        geminiModels,
			},
		},
	})
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
	origLines, _ := legacyhandlers.HOCRToLines(originalHOCR)
	newLines, _ := legacyhandlers.HOCRToLines(correctedHOCR)
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
	re := regexp.MustCompile(`ocr_page[^>]*title=['"]bbox\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)`)
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

	http.ServeFile(w, r, filepath.Join(h.webDir, "index.html"))
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

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}
