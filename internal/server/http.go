package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	legacyhandlers "github.com/lehigh-university-libraries/hOCRedit/internal/handlers"
	"github.com/lehigh-university-libraries/hOCRedit/internal/store"
)

type Handler struct {
	sessions *store.SessionStore
	mux      *http.ServeMux
	webDir   string
	legacy   *legacyhandlers.Handler
}

func NewHandler(sessions *store.SessionStore) *Handler {
	webDir := detectWebDir()
	if webDir == "" {
		slog.Warn("web assets directory not found; root path will return 404")
	} else {
		slog.Info("serving web assets", "dir", webDir)
	}

	handler := &Handler{
		sessions: sessions,
		webDir:   webDir,
		legacy:   legacyhandlers.New(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.handleHealth)
	mux.HandleFunc("GET /v1/sessions", handler.handleListSessions)
	mux.HandleFunc("POST /v1/sessions", handler.handleCreateSession)
	mux.HandleFunc("POST /v1/process/url", handler.handleProcessURL)
	mux.HandleFunc("POST /v1/process/upload", handler.handleProcessUpload)
	mux.Handle("GET /static/uploads/", http.StripPrefix("/static/uploads/", http.FileServer(http.Dir("uploads"))))
	mux.HandleFunc("GET /", handler.handleWeb)
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
	var req struct {
		ImageURL string `json:"image_url"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.ImageURL) == "" {
		writeError(w, http.StatusBadRequest, "image_url is required")
		return
	}

	result, err := h.legacy.ProcessImageURLWithModel(req.ImageURL, req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.renderProcessedOutput(w, r, result)
}

func (h *Handler) handleProcessUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	file, fileHeader, err := extractUploadFile(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer file.Close()

	fileData, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read upload")
		return
	}

	model := r.FormValue("model")
	result, err := h.legacy.ProcessImageUploadWithModel(fileHeader.Filename, fileData, model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.renderProcessedOutput(w, r, result)
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
