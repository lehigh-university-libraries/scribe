package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/models"
)

func (h *Handler) HandleHOCRUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		h.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		SessionID string `json:"session_id"`
		ImageID   string `json:"image_id"`
		HOCR      string `json:"hocr"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.writeError(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	session, ok := h.getSessionOrError(w, request.SessionID)
	if !ok {
		return
	}

	for i, image := range session.Images {
		if image.ID == request.ImageID {
			session.Images[i].CorrectedHOCR = request.HOCR
			session.Images[i].Completed = true
			break
		}
	}

	h.sessionStore.Set(request.SessionID, session)
	h.writeJSON(w, map[string]string{"status": "success"})
}

func (h *Handler) HandleHOCRParse(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		h.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		HOCR string `json:"hocr"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.writeError(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	words, err := hocr.ParseHOCRWords(request.HOCR)
	if err != nil {
		slog.Error("Unable to parse hocr", "hocr", request.HOCR, "err", err)
		h.writeError(w, "Failed to parse hOCR: "+err.Error(), http.StatusBadRequest)
		return
	}

	response := struct {
		Words []models.HOCRWord `json:"words"`
	}{
		Words: words,
	}

	h.writeJSON(w, response)
}
