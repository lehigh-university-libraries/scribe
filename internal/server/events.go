package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"golang.org/x/sync/errgroup"
)

type cloudEvent struct {
	SpecVersion     string         `json:"specversion"`
	ID              string         `json:"id"`
	Source          string         `json:"source"`
	Type            string         `json:"type"`
	Subject         string         `json:"subject,omitempty"`
	Time            string         `json:"time"`
	DataContentType string         `json:"datacontenttype,omitempty"`
	Data            map[string]any `json:"data,omitempty"`
}

func (h *Handler) publishEvent(eventType, subject string, data map[string]any) {
	evt := h.newCloudEvent(eventType, subject, data)
	h.publishCloudEvent(evt, true)
}

func (h *Handler) newCloudEvent(eventType, subject string, data map[string]any) cloudEvent {
	return cloudEvent{
		SpecVersion:     "1.0",
		ID:              uuid.NewString(),
		Source:          "/scribe",
		Type:            eventType,
		Subject:         subject,
		Time:            time.Now().UTC().Format(time.RFC3339Nano),
		DataContentType: "application/json",
		Data:            data,
	}
}

func (h *Handler) publishCloudEvent(evt cloudEvent, enqueueWebhook bool) {
	if evt.ID == "" {
		return
	}
	if enqueueWebhook {
		h.enqueueWebhooks(evt)
	}
}

func (h *Handler) enqueueWebhooks(evt cloudEvent) {
	if h.transcriptionJobs == nil {
		return
	}
	body, err := json.Marshal(evt)
	if err != nil {
		slog.Warn("Failed to marshal webhook event", "event_type", evt.Type, "error", err)
		return
	}
	if err := h.transcriptionJobs.EnqueueWebhookEvent(h.backgroundContext(), evt.ID, evt.Type, evt.Subject, string(body), h.webhookURLs); err != nil {
		slog.Warn("Failed to enqueue webhook event", "event_type", evt.Type, "event_id", evt.ID, "error", err)
	}
}

// StartWebhookDispatcher starts durable webhook delivery workers until ctx is cancelled.
func (h *Handler) StartWebhookDispatcher(ctx context.Context) {
	if h.transcriptionJobs == nil || len(h.webhookURLs) == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		retentionTicker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		defer retentionTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.dispatchWebhookBatch(ctx)
			case <-retentionTicker.C:
				if err := h.transcriptionJobs.RetainWebhookEvents(ctx, 30*24*time.Hour); err != nil {
					slog.Warn("Failed to retain webhook events", "error", err)
				}
			}
		}
	}()
}

func (h *Handler) StartProviderCallAuditRetention(ctx context.Context) {
	if h.providerCallAudits == nil {
		return
	}
	retention := config.Get().Config.Audit.ProviderCallRetention
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := h.providerCallAudits.Retain(ctx, retention); err != nil {
					slog.Warn("Failed to retain provider call audits", "error", err)
				}
			}
		}
	}()
}

func (h *Handler) dispatchWebhookBatch(ctx context.Context) {
	deliveries, err := h.transcriptionJobs.ClaimWebhookDeliveries(ctx, 10)
	if err != nil {
		slog.Warn("Failed to claim webhook deliveries", "error", err)
		return
	}
	g, groupCtx := errgroup.WithContext(ctx)
	g.SetLimit(5)
	for _, delivery := range deliveries {
		delivery := delivery
		g.Go(func() error {
			if err := h.deliverWebhook(groupCtx, delivery.TargetURL, []byte(delivery.BodyJSON)); err != nil {
				slog.Warn("Webhook delivery failed", "target", delivery.TargetURL, "event_type", delivery.EventType, "event_id", delivery.EventID, "error", err)
				markCtx := ctx
				var cancel context.CancelFunc
				if groupCtx.Err() != nil {
					markCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
				}
				if cancel != nil {
					defer cancel()
				}
				_ = h.transcriptionJobs.MarkWebhookDeliveryFailed(markCtx, delivery.ID, delivery.LeaseOwner, err.Error())
				return nil
			}
			_ = h.transcriptionJobs.MarkWebhookDeliveryDelivered(ctx, delivery.ID, delivery.LeaseOwner)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		slog.Warn("Webhook batch stopped", "error", err)
	}
}

func (h *Handler) deliverWebhook(ctx context.Context, targetURL string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	resp, err := safehttp.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func parseEventTypes(values []string) map[string]struct{} {
	types := make(map[string]struct{})
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			trimmed := strings.TrimSpace(raw)
			if trimmed != "" {
				types[trimmed] = struct{}{}
			}
		}
	}
	return types
}

func (h *Handler) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if h.transcriptionJobs == nil {
		writeError(w, http.StatusServiceUnavailable, "event stream unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	if h.auth != nil {
		if principal, ok := auth.PrincipalFromContext(r.Context()); !ok || principal.Anonymous() {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
	}

	query := r.URL.Query()
	types := parseEventTypes(query["type"])
	itemImageID := strings.TrimSpace(query.Get("item_image_id"))
	subjectPrefix := ""
	if itemImageID != "" {
		if _, err := strconv.ParseUint(itemImageID, 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, "invalid item_image_id")
			return
		}
		subjectPrefix = fmt.Sprintf("item-images/%s", itemImageID)
	}

	workspaceID := h.currentWorkspaceID(r.Context())
	visibility := newEventVisibilityCache()
	matches := func(evt cloudEvent) bool {
		if len(types) > 0 {
			if _, ok := types[evt.Type]; !ok {
				return false
			}
		}
		if subjectPrefix != "" && !strings.HasPrefix(evt.Subject, subjectPrefix) {
			return false
		}
		if !h.eventVisibleToWorkspaceCached(r.Context(), workspaceID, evt, visibility) {
			return false
		}
		return true
	}

	lastOutboxID, err := h.transcriptionJobs.EventOutboxHighWater(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "event stream unavailable")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	pollTicker := time.NewTicker(2 * time.Second)
	defer pollTicker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = w.Write([]byte(": keep-alive\n\n"))
			flusher.Flush()
		case <-pollTicker.C:
			events, err := h.transcriptionJobs.ListEventOutboxAfterForWorkspace(ctx, lastOutboxID, workspaceID, 100)
			if err != nil {
				slog.Warn("Failed to poll event outbox", "error", err)
				continue
			}
			for _, outboxEvent := range events {
				if outboxEvent.ID > lastOutboxID {
					lastOutboxID = outboxEvent.ID
				}
				evt, err := cloudEventFromOutbox(outboxEvent.EventID, outboxEvent.EventType, outboxEvent.Subject, outboxEvent.BodyJSON)
				if err != nil {
					slog.Warn("Failed to decode event outbox body", "outbox_id", outboxEvent.ID, "error", err)
					continue
				}
				if !matches(evt) {
					continue
				}
				if err := writeSSEEvent(w, evt); err != nil {
					slog.Warn("Failed to write SSE event", "event_id", evt.ID, "error", err)
					return
				}
				flusher.Flush()
			}
		}
	}
}

func cloudEventFromOutbox(eventID, eventType, subject, bodyJSON string) (cloudEvent, error) {
	decoder := json.NewDecoder(strings.NewReader(bodyJSON))
	decoder.UseNumber()
	var evt cloudEvent
	if err := decoder.Decode(&evt); err != nil {
		return cloudEvent{}, err
	}
	if evt.ID == "" {
		evt.ID = eventID
	}
	if evt.Type == "" {
		evt.Type = eventType
	}
	if evt.Subject == "" {
		evt.Subject = subject
	}
	return evt, nil
}

func writeSSEEvent(w http.ResponseWriter, evt cloudEvent) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", evt.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	return nil
}

func subjectForItemImage(itemImageID uint64) string {
	return fmt.Sprintf("item-images/%d", itemImageID)
}

func subjectForAnnotation(itemImageID uint64, annotationID string) string {
	return fmt.Sprintf("item-images/%d/annotations/%s", itemImageID, annotationID)
}

type eventVisibilityCache struct {
	mu              sync.Mutex
	itemImages      map[uint64]bool
	itemImagesKnown map[uint64]struct{}
	items           map[string]bool
	itemsKnown      map[string]struct{}
}

func newEventVisibilityCache() *eventVisibilityCache {
	return &eventVisibilityCache{
		itemImages:      make(map[uint64]bool),
		itemImagesKnown: make(map[uint64]struct{}),
		items:           make(map[string]bool),
		itemsKnown:      make(map[string]struct{}),
	}
}

func (h *Handler) eventVisibleToWorkspaceCached(ctx context.Context, workspaceID uint64, evt cloudEvent, cache *eventVisibilityCache) bool {
	if itemImageID, ok := eventItemImageID(evt); ok {
		cache.mu.Lock()
		if _, known := cache.itemImagesKnown[itemImageID]; known {
			visible := cache.itemImages[itemImageID]
			cache.mu.Unlock()
			return visible
		}
		cache.mu.Unlock()

		visible, err := h.items.WorkspaceOwnsItemImage(ctx, workspaceID, itemImageID)

		cache.mu.Lock()
		cache.itemImagesKnown[itemImageID] = struct{}{}
		cache.itemImages[itemImageID] = err == nil && visible
		cache.mu.Unlock()
		return err == nil && visible
	}
	if itemID, ok := eventItemID(evt); ok {
		cache.mu.Lock()
		if _, known := cache.itemsKnown[itemID]; known {
			visible := cache.items[itemID]
			cache.mu.Unlock()
			return visible
		}
		cache.mu.Unlock()

		visible, err := h.items.WorkspaceOwnsItem(ctx, workspaceID, itemID)

		cache.mu.Lock()
		cache.itemsKnown[itemID] = struct{}{}
		cache.items[itemID] = err == nil && visible
		cache.mu.Unlock()
		return err == nil && visible
	}
	return false
}

func eventItemImageID(evt cloudEvent) (uint64, bool) {
	for _, key := range []string{"itemImageId", "item_image_id"} {
		if value, ok := evt.Data[key]; ok {
			if itemImageID, ok := toUint64(value); ok {
				return itemImageID, true
			}
		}
	}
	if strings.HasPrefix(evt.Subject, "item-images/") {
		remainder := strings.TrimPrefix(evt.Subject, "item-images/")
		itemImageIDPart := remainder
		if slash := strings.Index(itemImageIDPart, "/"); slash >= 0 {
			itemImageIDPart = itemImageIDPart[:slash]
		}
		if itemImageID, err := strconv.ParseUint(strings.TrimSpace(itemImageIDPart), 10, 64); err == nil {
			return itemImageID, true
		}
	}
	return 0, false
}

func eventItemID(evt cloudEvent) (string, bool) {
	for _, key := range []string{"itemId", "item_id"} {
		if value, ok := evt.Data[key]; ok {
			switch typed := value.(type) {
			case string:
				typed = strings.TrimSpace(typed)
				if typed != "" {
					return typed, true
				}
			}
		}
	}
	return "", false
}

func toUint64(value any) (uint64, bool) {
	switch typed := value.(type) {
	case float64:
		return uint64(typed), true
	case string:
		parsed, err := strconv.ParseUint(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	case json.Number:
		parsed, err := strconv.ParseUint(typed.String(), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
