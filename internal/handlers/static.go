package handlers

import (
	"log/slog"
	"net/http"
	"strings"
)

func (h *Handler) HandleStatic(w http.ResponseWriter, r *http.Request) {
	filepath := strings.TrimPrefix(r.URL.Path, "/static/")

	if strings.HasPrefix(filepath, "uploads/") {
		http.ServeFile(w, r, filepath)
		return
	}

	// Extract the file path after /static/
	if filepath == "" {
		filepath = "index.html"
	}

	// Check if image URL parameter is provided
	imageURL := r.URL.Query().Get("image")
	if imageURL != "" {
		// Create session from image URL
		provider := r.URL.Query().Get("provider")
		model := r.URL.Query().Get("model")
		sessionID, err := h.createSessionFromURL(imageURL, provider, model)
		if err != nil {
			slog.Error("Failed to create session from URL", "url", imageURL, "error", err)
			http.Error(w, "Failed to process image URL: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Redirect to the session
		http.Redirect(w, r, "/hocr/?session="+sessionID, http.StatusFound)
		return
	}

	// Check if Drupal node ID parameter is provided
	nid := r.URL.Query().Get("nid")
	if nid != "" {
		// Create session from Drupal node
		sessionID, err := h.createSessionFromDrupalNode(nid)
		if err != nil {
			slog.Error("Failed to create session from Drupal node", "nid", nid, "error", err)
			http.Error(w, "Failed to process Drupal node: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Redirect to the session
		http.Redirect(w, r, "/hocr/?session="+sessionID, http.StatusFound)
		return
	}

	// Prevent directory traversal attacks
	if strings.Contains(filepath, "..") {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	// Set appropriate content type based on file extension
	switch {
	case strings.HasSuffix(filepath, ".css"):
		w.Header().Set("Content-Type", "text/css")
	case strings.HasSuffix(filepath, ".js"):
		w.Header().Set("Content-Type", "application/javascript")
	case strings.HasSuffix(filepath, ".html"):
		w.Header().Set("Content-Type", "text/html")
	}

	// Serve files from the static directory
	fullPath := "static/" + filepath
	http.ServeFile(w, r, fullPath)
}
