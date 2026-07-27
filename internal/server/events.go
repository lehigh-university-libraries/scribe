package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/store"
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

const retentionOperationTimeout = 30 * time.Second

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
		slog.Warn("Failed to marshal webhook event", "event_type", evt.Type, "error_type", safeLogErrorType(err))
		return
	}
	if err := h.transcriptionJobs.EnqueueWebhookEvent(h.backgroundContext(), evt.ID, evt.Type, evt.Subject, string(body)); err != nil {
		slog.Warn("Failed to enqueue webhook event", "event_type", evt.Type, "event_id", evt.ID, "error_type", safeLogErrorType(err))
	}
}

// StartWebhookDispatcher starts durable webhook delivery workers until ctx is cancelled.
func (h *Handler) StartWebhookDispatcher(ctx context.Context) {
	if h.transcriptionJobs == nil {
		return
	}
	h.startWebhookEventRetention(ctx)
	h.startBackgroundWorker(func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.dispatchWebhookBatch(ctx)
			}
		}
	})
}

func (h *Handler) startWebhookEventRetention(ctx context.Context) {
	h.startBackgroundWorker(func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			h.retainWebhookEvents(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (h *Handler) retainWebhookEvents(ctx context.Context) {
	operationCtx, cancel := context.WithTimeout(ctx, retentionOperationTimeout)
	defer cancel()
	if err := h.transcriptionJobs.RetainWebhookEvents(operationCtx, 30*24*time.Hour); err != nil && ctx.Err() == nil {
		slog.Warn("Failed to retain webhook events", "error_type", safeLogErrorType(err))
	}
}

// StartExternalRequestRetention periodically removes expired idempotency
// records while preserving every operation with a live lease.
func (h *Handler) StartExternalRequestRetention(ctx context.Context) {
	if h.transcriptionJobs == nil {
		return
	}
	retention := config.Get().Config.Processing.ExternalRequestRetention
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	h.startBackgroundWorker(func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			h.retainExternalRequests(ctx, retention)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (h *Handler) retainExternalRequests(ctx context.Context, retention time.Duration) {
	operationCtx, cancel := context.WithTimeout(ctx, retentionOperationTimeout)
	defer cancel()
	if err := h.transcriptionJobs.RetainExternalRequests(operationCtx, retention); err != nil && ctx.Err() == nil {
		slog.Warn("Failed to retain external requests", "error_type", safeLogErrorType(err))
	}
}

func (h *Handler) StartProviderCallAuditRetention(ctx context.Context) {
	if h.providerCallAudits == nil {
		return
	}
	retention := config.Get().Config.Audit.ProviderCallRetention
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	h.startBackgroundWorker(func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			h.retainProviderCallAudits(ctx, retention)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (h *Handler) retainProviderCallAudits(ctx context.Context, retention time.Duration) {
	operationCtx, cancel := context.WithTimeout(ctx, retentionOperationTimeout)
	defer cancel()
	if err := h.providerCallAudits.Retain(operationCtx, retention); err != nil && ctx.Err() == nil {
		slog.Warn("Failed to retain provider call audits", "error_type", safeLogErrorType(err))
	}
}

func (h *Handler) dispatchWebhookBatch(ctx context.Context) {
	deliveries, err := h.transcriptionJobs.ClaimWebhookDeliveries(ctx, 10)
	if err != nil {
		slog.Warn("Failed to claim webhook deliveries", "error_type", safeLogErrorType(err))
		return
	}
	g, groupCtx := errgroup.WithContext(ctx)
	g.SetLimit(5)
	for _, delivery := range deliveries {
		delivery := delivery
		g.Go(func() error {
			if err := h.deliverWebhook(groupCtx, delivery.TargetURL, delivery.SigningSecret, []byte(delivery.BodyJSON)); err != nil {
				_, targetID := webhookTargetAuditFields(delivery.TargetURL)
				failure := safeWebhookFailure(err)
				slog.Warn("Webhook delivery failed", "target_id", targetID, "event_type", delivery.EventType, "event_id", delivery.EventID, "failure", failure)
				markCtx := ctx
				var cancel context.CancelFunc
				if groupCtx.Err() != nil {
					markCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
				}
				if cancel != nil {
					defer cancel()
				}
				_ = h.transcriptionJobs.MarkWebhookDeliveryFailed(markCtx, delivery.ID, delivery.LeaseOwner, failure)
				return nil
			}
			_ = h.transcriptionJobs.MarkWebhookDeliveryDelivered(ctx, delivery.ID, delivery.LeaseOwner)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		slog.Warn("Webhook batch stopped", "error_type", safeLogErrorType(err))
	}
}

const (
	webhookTimestampHeader = "X-Scribe-Timestamp"
	webhookSignatureHeader = "X-Scribe-Signature"
)

func (h *Handler) deliverWebhook(ctx context.Context, targetURL string, signingSecret, body []byte) error {
	if len(signingSecret) < store.MinWebhookSigningSecretBytes || len(signingSecret) > store.MaxWebhookSigningSecretBytes {
		return fmt.Errorf("webhook signing secret is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	req.Header.Set(webhookTimestampHeader, timestamp)
	req.Header.Set(webhookSignatureHeader, webhookSignature(signingSecret, timestamp, body))
	resp, err := safehttp.DoNoRedirect(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// webhookSignature signs the exact timestamp header, one ASCII period, and
// the exact request body. The version prefix permits future algorithm rotation
// without accepting an ambiguous signature representation.
func webhookSignature(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func verifyWebhookSignature(secret []byte, timestamp, signature string, body []byte) bool {
	if len(secret) < store.MinWebhookSigningSecretBytes || len(secret) > store.MaxWebhookSigningSecretBytes || strings.TrimSpace(timestamp) != timestamp {
		return false
	}
	if _, err := strconv.ParseInt(timestamp, 10, 64); err != nil {
		return false
	}
	expected := webhookSignature(secret, timestamp, body)
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature)))
}

func webhookTargetAuditFields(raw string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	origin := "invalid"
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		origin = strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
	}
	sum := sha256.Sum256([]byte(raw))
	return origin, fmt.Sprintf("%x", sum[:6])
}

func safeWebhookFailure(err error) string {
	if err == nil {
		return "webhook request failed"
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "webhook request timed out"
	}
	message := err.Error()
	if strings.HasPrefix(message, "status ") {
		return message
	}
	return "webhook request failed"
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
	var scopedItemImageID uint64
	if h.auth != nil {
		if principal, ok := auth.PrincipalFromContext(r.Context()); !ok || principal.Anonymous() {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		} else {
			scopedItemImageID = principal.ScopedItemImageID
		}
	}

	query := r.URL.Query()
	types := parseEventTypes(query["type"])
	itemImageID := strings.TrimSpace(query.Get("item_image_id"))
	if scopedItemImageID > 0 {
		expected := strconv.FormatUint(scopedItemImageID, 10)
		if itemImageID != "" && itemImageID != expected {
			writeError(w, http.StatusForbidden, "permission denied")
			return
		}
		itemImageID = expected
	}
	subjectPrefix := ""
	if itemImageID != "" {
		if _, err := strconv.ParseUint(itemImageID, 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, "invalid item_image_id")
			return
		}
		subjectPrefix = fmt.Sprintf("item-images/%s", itemImageID)
	}

	workspaceID := h.currentWorkspaceID(r.Context())
	release, ok := h.sseLimiter.Acquire(workspaceID)
	if !ok {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "too many event stream connections")
		return
	}
	defer release()
	visibility := newEventVisibilityCache()
	matches := func(evt cloudEvent) bool {
		if len(types) > 0 {
			if _, ok := types[evt.Type]; !ok {
				return false
			}
		}
		if subjectPrefix != "" && evt.Subject != subjectPrefix && !strings.HasPrefix(evt.Subject, subjectPrefix+"/") {
			return false
		}
		if !h.eventVisibleToWorkspaceCached(r.Context(), workspaceID, evt, visibility) {
			return false
		}
		return true
	}

	highWater, err := h.transcriptionJobs.EventOutboxHighWaterForWorkspace(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "event stream unavailable")
		return
	}
	lastOutboxID, err := eventStreamCursor(r.Header.Get("Last-Event-ID"), highWater)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := writeSSEFrame(w, flusher, func() error {
		return writeSSEControl(w, "dev.scribe.stream.ready", lastOutboxID)
	}); err != nil {
		return
	}

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
			if err := writeSSEFrame(w, flusher, func() error {
				_, err := w.Write([]byte(": keep-alive\n\n"))
				return err
			}); err != nil {
				return
			}
		case <-pollTicker.C:
			events, err := h.transcriptionJobs.ListEventOutboxAfterForWorkspace(ctx, lastOutboxID, workspaceID, 100)
			if err != nil {
				slog.Warn("Failed to poll event outbox", "error_type", safeLogErrorType(err))
				continue
			}
			batchStart := lastOutboxID
			lastEmittedID := uint64(0)
			for _, outboxEvent := range events {
				if outboxEvent.ID > lastOutboxID {
					lastOutboxID = outboxEvent.ID
				}
				evt, err := cloudEventFromOutbox(outboxEvent.EventID, outboxEvent.EventType, outboxEvent.Subject, outboxEvent.BodyJSON)
				if err != nil {
					slog.Warn("Failed to decode event outbox body", "outbox_id", outboxEvent.ID, "error_type", safeLogErrorType(err))
					continue
				}
				if !matches(evt) {
					continue
				}
				if err := writeSSEFrame(w, flusher, func() error {
					return writeSSEEvent(w, outboxEvent.ID, evt)
				}); err != nil {
					slog.Warn("Failed to write SSE event", "event_id", evt.ID, "error_type", safeLogErrorType(err))
					return
				}
				lastEmittedID = outboxEvent.ID
			}
			if lastOutboxID > batchStart && lastEmittedID != lastOutboxID {
				if err := writeSSEFrame(w, flusher, func() error {
					return writeSSEControl(w, "dev.scribe.stream.checkpoint", lastOutboxID)
				}); err != nil {
					return
				}
			}
		}
	}
}

const sseWriteTimeout = 10 * time.Second

func writeSSEFrame(w http.ResponseWriter, flusher http.Flusher, write func() error) error {
	controller := http.NewResponseController(w)
	deadlineSet := false
	if err := controller.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err == nil {
		deadlineSet = true
	} else if !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if deadlineSet {
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	}
	if err := write(); err != nil {
		return err
	}
	flusher.Flush()
	return nil
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

func eventStreamCursor(lastEventID string, highWater uint64) (uint64, error) {
	lastEventID = strings.TrimSpace(lastEventID)
	if lastEventID == "" {
		return highWater, nil
	}
	cursor, err := strconv.ParseUint(lastEventID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid Last-Event-ID")
	}
	if cursor > highWater {
		return 0, fmt.Errorf("last-event-ID is ahead of the event stream")
	}
	return cursor, nil
}

func writeSSEControl(w http.ResponseWriter, eventType string, cursor uint64) error {
	if strings.ContainsAny(eventType, "\r\n") {
		return fmt.Errorf("invalid SSE event type")
	}
	payload, err := json.Marshal(map[string]string{"cursor": strconv.FormatUint(cursor, 10)})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", cursor, eventType, payload); err != nil { // #nosec G705 -- eventType rejects CR/LF and payload is JSON encoded.
		return err
	}
	return nil
}

func writeSSEEvent(w http.ResponseWriter, cursor uint64, evt cloudEvent) error {
	if cursor == 0 || strings.ContainsAny(evt.Type, "\r\n") {
		return fmt.Errorf("invalid SSE event identity")
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\n", cursor, evt.Type); err != nil { // #nosec G705 -- evt.Type rejects CR/LF above, so it cannot terminate or inject an SSE field; this is not an HTML sink.
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
