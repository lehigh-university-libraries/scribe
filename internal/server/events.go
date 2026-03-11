package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

type eventSubscription struct {
	ch     chan cloudEvent
	filter func(cloudEvent) bool
}

type eventBroker struct {
	mu          sync.RWMutex
	nextSubID   uint64
	nextEventID uint64
	subs        map[uint64]eventSubscription
}

func newEventBroker() *eventBroker {
	return &eventBroker{subs: make(map[uint64]eventSubscription)}
}

func (b *eventBroker) subscribe(filter func(cloudEvent) bool) (uint64, <-chan cloudEvent) {
	id := atomic.AddUint64(&b.nextSubID, 1)
	ch := make(chan cloudEvent, 32)
	b.mu.Lock()
	b.subs[id] = eventSubscription{ch: ch, filter: filter}
	b.mu.Unlock()
	return id, ch
}

func (b *eventBroker) unsubscribe(id uint64) {
	b.mu.Lock()
	sub, ok := b.subs[id]
	if ok {
		delete(b.subs, id)
		close(sub.ch)
	}
	b.mu.Unlock()
}

func (b *eventBroker) publish(evt cloudEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		if sub.filter != nil && !sub.filter(evt) {
			continue
		}
		select {
		case sub.ch <- evt:
		default:
		}
	}
}

func (h *Handler) publishEvent(eventType, subject string, data map[string]any) {
	if h.events == nil {
		return
	}
	id := atomic.AddUint64(&h.events.nextEventID, 1)
	evt := cloudEvent{
		SpecVersion:     "1.0",
		ID:              fmt.Sprintf("scribe-%d-%d", time.Now().UnixNano(), id),
		Source:          "/scribe",
		Type:            eventType,
		Subject:         subject,
		Time:            time.Now().UTC().Format(time.RFC3339Nano),
		DataContentType: "application/json",
		Data:            data,
	}
	h.events.publish(evt)
	h.deliverWebhooks(evt)
}

func parseWebhookURLs(raw string) []string {
	var urls []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			urls = append(urls, trimmed)
		}
	}
	return urls
}

func (h *Handler) deliverWebhooks(evt cloudEvent) {
	if len(h.webhookURLs) == 0 || h.webhookClient == nil {
		return
	}
	body, err := json.Marshal(evt)
	if err != nil {
		slog.Warn("Failed to marshal webhook event", "event_type", evt.Type, "error", err)
		return
	}
	for _, target := range h.webhookURLs {
		targetURL := target
		go func() {
			req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(body))
			if err != nil {
				slog.Warn("Failed to create webhook request", "target", targetURL, "event_type", evt.Type, "error", err)
				return
			}
			req.Header.Set("Content-Type", "application/cloudevents+json")
			resp, err := h.webhookClient.Do(req)
			if err != nil {
				slog.Warn("Webhook delivery failed", "target", targetURL, "event_type", evt.Type, "error", err)
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				slog.Warn("Webhook delivery returned non-2xx", "target", targetURL, "event_type", evt.Type, "status", resp.StatusCode)
			}
		}()
	}
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
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

	filter := func(evt cloudEvent) bool {
		if len(types) > 0 {
			if _, ok := types[evt.Type]; !ok {
				return false
			}
		}
		if subjectPrefix != "" && !strings.HasPrefix(evt.Subject, subjectPrefix) {
			return false
		}
		return true
	}

	subID, ch := h.events.subscribe(filter)
	defer h.events.unsubscribe(subID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = w.Write([]byte(": keep-alive\n\n"))
			flusher.Flush()
		case evt := <-ch:
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\n", evt.Type)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func subjectForItemImage(itemImageID uint64) string {
	return fmt.Sprintf("item-images/%d", itemImageID)
}

func subjectForAnnotation(itemImageID uint64, annotationID string) string {
	return fmt.Sprintf("item-images/%d/annotations/%s", itemImageID, annotationID)
}
