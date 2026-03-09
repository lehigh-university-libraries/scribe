package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/hOCRedit/internal/db"
	"github.com/lehigh-university-libraries/hOCRedit/internal/store"
)

func (h *Handler) handleListItems(w http.ResponseWriter, r *http.Request) {
	items, err := h.items.List(r.Context(), store.AnonymousUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleCreateItemREST(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string         `json:"name"`
		SourceType string         `json:"source_type"`
		SourceURL  string         `json:"source_url"`
		Metadata   map[string]any `json:"metadata"`
		ContextID  uint64         `json:"context_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = "Untitled Item"
	}
	if req.SourceType == "" {
		req.SourceType = "url"
	}

	itemID := time.Now().UTC().Format("20060102150405")
	metaJSON := ""
	if req.Metadata != nil {
		b, _ := json.Marshal(req.Metadata)
		metaJSON = string(b)
	}

	item, err := h.items.Create(r.Context(), db.CreateItemParams{
		ID:         itemID,
		UserID:     store.AnonymousUserID,
		Name:       req.Name,
		SourceType: req.SourceType,
		SourceURL:  req.SourceURL,
		Metadata:   metaJSON,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) handleGetItem(w http.ResponseWriter, r *http.Request) {
	itemID := strings.TrimSpace(r.PathValue("item_id"))
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "item_id is required")
		return
	}
	item, err := h.items.Get(r.Context(), itemID)
	if err != nil {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	itemID := strings.TrimSpace(r.PathValue("item_id"))
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "item_id is required")
		return
	}
	if err := h.items.Delete(r.Context(), itemID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
