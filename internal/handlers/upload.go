package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

func (h *Handler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		h.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if this is a JSON request with image URL
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		h.handleURLUpload(w, r)
		return
	}

	// Handle file upload
	h.handleFileUpload(w, r)
}

func (h *Handler) handleURLUpload(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ImageURL string `json:"image_url"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.writeError(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if request.ImageURL == "" {
		h.writeError(w, "image_url is required", http.StatusBadRequest)
		return
	}

	sessionID, err := h.createSessionFromURL(request.ImageURL, request.Provider, request.Model)
	if err != nil {
		h.writeError(w, "Failed to process image URL: "+err.Error(), http.StatusBadRequest)
		return
	}

	response := map[string]any{
		"session_id": sessionID,
		"message":    "Successfully processed image from URL",
		"images":     1,
		"cache_used": false,
		"source":     "url",
	}

	h.writeJSON(w, response)
}

func (h *Handler) handleFileUpload(w http.ResponseWriter, r *http.Request) {

	file, header, err := r.FormFile("files")
	if err != nil {
		file, header, err = r.FormFile("file")
		if err != nil {
			h.writeError(w, "Failed to read file: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	defer file.Close()

	// Extract provider and model from form data
	provider := r.FormValue("provider")
	model := r.FormValue("model")

	if err := h.ensureUploadsDir(); err != nil {
		h.writeError(w, "Failed to create uploads directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	fileData, err := io.ReadAll(file)
	if err != nil {
		h.writeError(w, "Failed to read file contents: "+err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := h.processImageFileWithConfig(fileData, header.Filename, provider, model)
	if err != nil {
		h.writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Use filename (without extension) as session name, with timestamp for uniqueness
	baseFilename := strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	sessionID := fmt.Sprintf("%s_%d", baseFilename, time.Now().Unix())

	config := SessionConfig{
		Provider: provider,
		Model:    model,
	}
	session := h.createImageSession(sessionID, result, config)
	h.sessionStore.Set(sessionID, session)

	response := map[string]any{
		"session_id": sessionID,
		"message":    "Successfully processed 1 file",
		"images":     1,
		"cache_used": h.wasCacheUsed(result.MD5Hash),
		"md5_hash":   result.MD5Hash,
	}

	h.writeJSON(w, response)
}

// HandleOCR processes an uploaded image and returns hOCR XML directly
func (h *Handler) HandleOCR(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		h.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract provider and model from form data or query params
	provider := r.FormValue("provider")
	if provider == "" {
		provider = r.URL.Query().Get("provider")
	}
	model := r.FormValue("model")
	if model == "" {
		model = r.URL.Query().Get("model")
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		file, header, err = r.FormFile("image")
		if err != nil {
			h.writeError(w, "Failed to read file: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	defer file.Close()

	if err := h.ensureUploadsDir(); err != nil {
		h.writeError(w, "Failed to create uploads directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	fileData, err := io.ReadAll(file)
	if err != nil {
		h.writeError(w, "Failed to read file contents: "+err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := h.processImageFileWithConfig(fileData, header.Filename, provider, model)
	if err != nil {
		h.writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return hOCR XML directly
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write([]byte(result.HOCRXML))
	if err != nil {
		h.writeError(w, "Failed to write response: "+err.Error(), http.StatusInternalServerError)
	}
}
