package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

type splitAnnotationIntoWordsRequest struct {
	AnnotationJSON string   `json:"annotation_json"`
	Words          []string `json:"words"`
}

type splitAnnotationIntoWordsResponse struct {
	AnnotationPageJSON string `json:"annotation_page_json"`
}

type splitAnnotationIntoTwoLinesRequest struct {
	AnnotationJSON string `json:"annotation_json"`
	SplitAtWord    int    `json:"split_at_word"`
}

type splitAnnotationIntoTwoLinesResponse struct {
	AnnotationJSONs []string `json:"annotation_jsons"`
}

type mergeAnnotationsRequest struct {
	AnnotationJSONs []string `json:"annotation_jsons"`
}

type mergeAnnotationResponse struct {
	AnnotationJSON string `json:"annotation_json"`
}

type transcribeAnnotationRequest struct {
	AnnotationJSON string `json:"annotation_json"`
	ContextID      uint64 `json:"context_id"`
}

type transcribeAnnotationResponse struct {
	AnnotationJSON string `json:"annotation_json"`
}

type transcribeAnnotationPageRequest struct {
	AnnotationPageJSON string `json:"annotation_page_json"`
	ContextID          uint64 `json:"context_id"`
}

type transcribeAnnotationPageResponse struct {
	AnnotationPageJSON string `json:"annotation_page_json"`
}

type reprocessItemImageWithContextRequest struct {
	ItemImageID uint64 `json:"item_image_id"`
	ContextID   uint64 `json:"context_id"`
}

type reprocessItemImageWithContextResponse struct {
	SessionID   string `json:"session_id"`
	ItemImageID uint64 `json:"item_image_id"`
	ContextID   uint64 `json:"context_id"`
	ImageURL    string `json:"image_url"`
	HOCR        string `json:"hocr"`
	PlainText   string `json:"plain_text"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
}

func (h *Handler) handleSplitAnnotationIntoWords(w http.ResponseWriter, r *http.Request) {
	var req splitAnnotationIntoWordsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	anno, text, x1, y1, x2, y2, canvasURI, err := parseLineAnnotation(req.AnnotationJSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	words := req.Words
	if len(words) == 0 {
		words = strings.Fields(text)
	}
	if len(words) == 0 {
		writeError(w, http.StatusBadRequest, "line has no words")
		return
	}

	lineID := strings.TrimSpace(annStringValue(anno, "id"))
	width := maxInt(1, x2-x1)
	height := maxInt(1, y2-y1)
	wordWidth := maxInt(1, width/len(words))
	items := make([]any, 0, len(words))
	for i, word := range words {
		wx1 := x1 + i*wordWidth
		wx2 := wx1 + wordWidth
		if i == len(words)-1 {
			wx2 = x2
		}
		wordAnno := map[string]any{
			"id":              fmt.Sprintf("%s-word-%d", lineID, i+1),
			"type":            "Annotation",
			"textGranularity": "word",
			"motivation":      "commenting",
			"body": []any{
				map[string]any{
					"type":    "TextualBody",
					"purpose": "describing",
					"format":  "text/plain",
					"value":   strings.TrimSpace(word),
				},
			},
			"target": map[string]any{
				"source": map[string]any{"id": canvasURI, "type": "Canvas"},
				"selector": map[string]any{
					"type":       "FragmentSelector",
					"conformsTo": "http://www.w3.org/TR/media-frags/",
					"value":      fmt.Sprintf("xywh=%d,%d,%d,%d", wx1, y1, maxInt(1, wx2-wx1), height),
				},
			},
		}
		items = append(items, wordAnno)
	}
	page := map[string]any{
		"@context": annotationPageContexts(),
		"type":     "AnnotationPage",
		"items":    items,
	}
	b, _ := json.Marshal(page)
	writeJSON(w, http.StatusOK, splitAnnotationIntoWordsResponse{AnnotationPageJSON: string(b)})
}

func (h *Handler) handleSplitAnnotationIntoTwoLines(w http.ResponseWriter, r *http.Request) {
	var req splitAnnotationIntoTwoLinesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	anno, text, x1, y1, x2, y2, canvasURI, err := parseLineAnnotation(req.AnnotationJSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	words := strings.Fields(text)
	if len(words) < 2 {
		writeError(w, http.StatusBadRequest, "line needs at least 2 words to split")
		return
	}
	splitAt := req.SplitAtWord
	if splitAt <= 0 || splitAt >= len(words) {
		splitAt = len(words) / 2
	}
	textA := strings.Join(words[:splitAt], " ")
	textB := strings.Join(words[splitAt:], " ")

	lineID := strings.TrimSpace(annStringValue(anno, "id"))
	fullHeight := maxInt(2, y2-y1)
	h1 := fullHeight / 2
	h2 := fullHeight - h1
	annoA := buildLineAnnotation(fmt.Sprintf("%s-a", lineID), canvasURI, x1, y1, x2, y1+h1, textA)
	annoB := buildLineAnnotation(fmt.Sprintf("%s-b", lineID), canvasURI, x1, y1+h1, x2, y1+h1+h2, textB)
	b1, _ := json.Marshal(annoA)
	b2, _ := json.Marshal(annoB)
	writeJSON(w, http.StatusOK, splitAnnotationIntoTwoLinesResponse{AnnotationJSONs: []string{string(b1), string(b2)}})
}

func (h *Handler) handleMergeAnnotationsIntoLine(w http.ResponseWriter, r *http.Request) {
	h.handleMergeAnyToLine(w, r)
}

func (h *Handler) handleMergeWordsIntoLineAnnotation(w http.ResponseWriter, r *http.Request) {
	h.handleMergeAnyToLine(w, r)
}

func (h *Handler) handleMergeAnyToLine(w http.ResponseWriter, r *http.Request) {
	var req mergeAnnotationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(req.AnnotationJSONs) < 2 {
		writeError(w, http.StatusBadRequest, "at least two annotations are required")
		return
	}

	var (
		texts        []string
		canvasURI    string
		unionX1      = int(^uint(0) >> 1)
		unionY1      = int(^uint(0) >> 1)
		unionX2      = 0
		unionY2      = 0
	)
	for _, raw := range req.AnnotationJSONs {
		_, text, x1, y1, x2, y2, c, err := parseLineAnnotation(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if canvasURI == "" {
			canvasURI = c
		}
		texts = append(texts, strings.TrimSpace(text))
		if x1 < unionX1 {
			unionX1 = x1
		}
		if y1 < unionY1 {
			unionY1 = y1
		}
		if x2 > unionX2 {
			unionX2 = x2
		}
		if y2 > unionY2 {
			unionY2 = y2
		}
	}
	merged := buildLineAnnotation(
		fmt.Sprintf("merged-%s", annStableID(strings.Join(req.AnnotationJSONs, "|"))),
		canvasURI,
		unionX1, unionY1, unionX2, unionY2,
		strings.TrimSpace(strings.Join(texts, " ")),
	)
	b, _ := json.Marshal(merged)
	writeJSON(w, http.StatusOK, mergeAnnotationResponse{AnnotationJSON: string(b)})
}

func (h *Handler) handleTranscribeAnnotation(w http.ResponseWriter, r *http.Request) {
	var req transcribeAnnotationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.AnnotationJSON) == "" {
		writeError(w, http.StatusBadRequest, "annotation_json is required")
		return
	}
	var pctx store.Context
	var err error
	if req.ContextID > 0 {
		pctx, err = h.contexts.Get(r.Context(), req.ContextID)
	} else {
		pctx, _, err = h.contexts.Resolve(r.Context(), nil)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolve context: "+err.Error())
		return
	}
	enriched, err := h.enrichSingleAnnotation(r.Context(), req.AnnotationJSON, pctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, transcribeAnnotationResponse{AnnotationJSON: enriched})
}

func (h *Handler) handleTranscribeAnnotationPage(w http.ResponseWriter, r *http.Request) {
	var req transcribeAnnotationPageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.AnnotationPageJSON) == "" {
		writeError(w, http.StatusBadRequest, "annotation_page_json is required")
		return
	}
	var pctx store.Context
	var err error
	if req.ContextID > 0 {
		pctx, err = h.contexts.Get(r.Context(), req.ContextID)
	} else {
		pctx, _, err = h.contexts.Resolve(r.Context(), nil)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolve context: "+err.Error())
		return
	}
	enriched, err := h.enrichAnnotationPage(r.Context(), req.AnnotationPageJSON, pctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, transcribeAnnotationPageResponse{AnnotationPageJSON: enriched})
}

func (h *Handler) handleReprocessItemImageWithContext(w http.ResponseWriter, r *http.Request) {
	var req reprocessItemImageWithContextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ItemImageID == 0 {
		writeError(w, http.StatusBadRequest, "item_image_id is required")
		return
	}
	run, err := h.ocrRuns.GetByItemImageID(r.Context(), req.ItemImageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "ocr run not found")
		return
	}

	provider, model, err := h.resolveTranscriptionConfig(r.Context(), req.ContextID, "", "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.ocr.ProcessImageURLWithProviderAndModel(run.ImageURL, provider, model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var contextID *uint64
	if req.ContextID > 0 {
		contextID = &req.ContextID
	}
	itemImageID := req.ItemImageID
	if err := h.ocrRuns.Create(r.Context(), store.OCRRun{
		SessionID:    run.SessionID,
		ItemImageID:  &itemImageID,
		ContextID:    contextID,
		ImageURL:     result.ImageURL,
		Provider:     provider,
		Model:        model,
		OriginalHOCR: result.HOCR,
		OriginalText: result.PlainText,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := writeSessionHOCR(run.SessionID, "original.hocr", result.HOCR); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, reprocessItemImageWithContextResponse{
		SessionID:   run.SessionID,
		ItemImageID: req.ItemImageID,
		ContextID:   req.ContextID,
		ImageURL:    result.ImageURL,
		HOCR:        result.HOCR,
		PlainText:   result.PlainText,
		Provider:    provider,
		Model:       model,
	})
}

func parseLineAnnotation(raw string) (map[string]any, string, int, int, int, int, string, error) {
	var anno map[string]any
	if err := json.Unmarshal([]byte(raw), &anno); err != nil {
		return nil, "", 0, 0, 0, 0, "", fmt.Errorf("invalid annotation json")
	}
	anno = normalizeAnnotation(anno, "")
	canvasURI := extractCanvasURI(anno)
	if canvasURI == "" {
		return nil, "", 0, 0, 0, 0, "", fmt.Errorf("annotation missing canvas uri")
	}
	fragment := extractFragment(anno)
	if fragment == "" {
		return nil, "", 0, 0, 0, 0, "", fmt.Errorf("annotation missing bbox fragment")
	}
	x1, y1, x2, y2, err := parseXYWH(fragment)
	if err != nil {
		return nil, "", 0, 0, 0, 0, "", err
	}
	text := extractAnnotationText(anno)
	return anno, text, x1, y1, x2, y2, canvasURI, nil
}

func extractAnnotationText(anno map[string]any) string {
	body, _ := anno["body"].([]any)
	for _, b := range body {
		obj, _ := b.(map[string]any)
		v := strings.TrimSpace(annStringValue(obj, "value"))
		if v != "" {
			return v
		}
	}
	return ""
}

func buildLineAnnotation(id, canvasURI string, x1, y1, x2, y2 int, text string) map[string]any {
	return map[string]any{
		"id":              id,
		"type":            "Annotation",
		"textGranularity": "line",
		"motivation":      "commenting",
		"body": []any{
			map[string]any{
				"type":    "TextualBody",
				"purpose": "describing",
				"format":  "text/plain",
				"value":   strings.TrimSpace(text),
			},
		},
		"target": map[string]any{
			"source": map[string]any{"id": canvasURI, "type": "Canvas"},
			"selector": map[string]any{
				"type":       "FragmentSelector",
				"conformsTo": "http://www.w3.org/TR/media-frags/",
				"value":      fmt.Sprintf("xywh=%d,%d,%d,%d", x1, y1, maxInt(1, x2-x1), maxInt(1, y2-y1)),
			},
		},
	}
}
