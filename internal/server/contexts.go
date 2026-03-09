package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/lehigh-university-libraries/hOCRedit/internal/store"
)

func (h *Handler) handleListContexts(w http.ResponseWriter, r *http.Request) {
	systemOnly := strings.EqualFold(r.URL.Query().Get("system_only"), "true")
	contexts, err := h.contexts.List(r.Context(), systemOnly)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contexts": contexts})
}

func (h *Handler) handleCreateContext(w http.ResponseWriter, r *http.Request) {
	var c store.Context
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	created, err := h.contexts.Create(r.Context(), c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) handleGetContext(w http.ResponseWriter, r *http.Request) {
	id, err := parseContextID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := h.contexts.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "context not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) handleUpdateContext(w http.ResponseWriter, r *http.Request) {
	id, err := parseContextID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var c store.Context
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	c.ID = id
	updated, err := h.contexts.Update(r.Context(), c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) handleDeleteContext(w http.ResponseWriter, r *http.Request) {
	id, err := parseContextID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.contexts.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleListRules(w http.ResponseWriter, r *http.Request) {
	id, err := parseContextID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rules, err := h.contexts.ListRules(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (h *Handler) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	id, err := parseContextID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var rule store.ContextSelectionRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	rule.ContextID = id
	created, err := h.contexts.CreateRule(r.Context(), rule)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.PathValue("rule_id"))
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule_id")
		return
	}
	if err := h.contexts.DeleteRule(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleResolveContext(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	c, isDefault, err := h.contexts.Resolve(r.Context(), req.Metadata)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"context":    c,
		"is_default": isDefault,
	})
}

func parseContextID(r *http.Request) (uint64, error) {
	raw := strings.TrimSpace(r.PathValue("context_id"))
	return strconv.ParseUint(raw, 10, 64)
}
