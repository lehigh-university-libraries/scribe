package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/metrics"
	"github.com/lehigh-university-libraries/scribe/internal/models"
)

func (h *Handler) HandleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		sessions := h.sessionStore.GetAll()
		sessionList := make([]*models.CorrectionSession, 0, len(sessions))
		for _, session := range sessions {
			sessionList = append(sessionList, session)
		}
		h.writeJSON(w, sessionList)
	default:
		h.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) HandleSessionDetail(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/api/sessions/")

	if strings.HasSuffix(sessionID, "/metrics") {
		sessionID = strings.TrimSuffix(sessionID, "/metrics")
		if r.Method == "POST" {
			h.handleMetrics(w, r, sessionID)
			return
		}
	}

	session, ok := h.getSessionOrError(w, sessionID)
	if !ok {
		return
	}

	switch r.Method {
	case "GET":
		h.writeJSON(w, session)
	case "PUT":
		var updatedSession models.CorrectionSession
		if err := json.NewDecoder(r.Body).Decode(&updatedSession); err != nil {
			h.writeError(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		h.sessionStore.Set(sessionID, &updatedSession)
		h.writeJSON(w, updatedSession)
	default:
		h.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request, _ string) {
	var request struct {
		Original  string `json:"original"`
		Corrected string `json:"corrected"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.writeError(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	metricsResult := metrics.CalculateAccuracyMetrics(request.Original, request.Corrected)
	h.writeJSON(w, metricsResult)
}
