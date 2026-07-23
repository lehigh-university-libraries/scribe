package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

type deadlineResponseRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (r *deadlineResponseRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

var _ http.Flusher = (*deadlineResponseRecorder)(nil)

func TestEventStreamCursor(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		highWater uint64
		want      uint64
		wantErr   bool
	}{
		{name: "new connection starts at high water", highWater: 42, want: 42},
		{name: "resume", header: " 17 ", highWater: 42, want: 17},
		{name: "zero", header: "0", highWater: 42, want: 0},
		{name: "malformed", header: "event-id", highWater: 42, wantErr: true},
		{name: "future", header: "43", highWater: 42, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := eventStreamCursor(test.header, test.highWater)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("eventStreamCursor(%q, %d) = %d, %v", test.header, test.highWater, got, err)
			}
		})
	}
}

func TestWriteSSEEventIncludesResumeCursor(t *testing.T) {
	output := httptest.NewRecorder()
	event := cloudEvent{ID: "event-1", Type: "dev.scribe.transcription.completed"}
	if err := writeSSEEvent(output, 27, event); err != nil {
		t.Fatalf("writeSSEEvent: %v", err)
	}
	encoded := output.Body.String()
	for _, expected := range []string{"id: 27\n", "event: dev.scribe.transcription.completed\n", `"id":"event-1"`} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("SSE output %q does not contain %q", encoded, expected)
		}
	}
	if err := writeSSEEvent(output, 0, event); err == nil {
		t.Fatal("writeSSEEvent accepted a zero outbox cursor")
	}
	for _, eventType := range []string{
		"dev.scribe.valid\nevent: injected",
		"dev.scribe.valid\rdata: injected",
	} {
		injectionOutput := httptest.NewRecorder()
		event.Type = eventType
		if err := writeSSEEvent(injectionOutput, 27, event); err == nil {
			t.Fatalf("writeSSEEvent accepted event type %q", eventType)
		}
		if injectionOutput.Body.Len() != 0 {
			t.Fatalf("writeSSEEvent emitted data for invalid event type %q: %q", eventType, injectionOutput.Body.String())
		}
	}
}

func TestWriteSSEControlIncludesCursor(t *testing.T) {
	output := httptest.NewRecorder()
	if err := writeSSEControl(output, "dev.scribe.stream.ready", 31); err != nil {
		t.Fatalf("writeSSEControl: %v", err)
	}
	encoded := output.Body.String()
	if !strings.Contains(encoded, "id: 31\n") || !strings.Contains(encoded, `"cursor":"31"`) {
		t.Fatalf("control SSE output = %q", encoded)
	}
}

func TestWriteSSEFrameBoundsAndClearsEveryWriteDeadline(t *testing.T) {
	output := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := writeSSEFrame(output, output, func() error {
		return writeSSEControl(output, "dev.scribe.stream.ready", 31)
	}); err != nil {
		t.Fatalf("writeSSEFrame: %v", err)
	}
	if len(output.deadlines) != 2 {
		t.Fatalf("write deadlines = %v, want set and clear", output.deadlines)
	}
	if output.deadlines[0].IsZero() || !output.deadlines[1].IsZero() {
		t.Fatalf("write deadlines = %v, want nonzero then zero", output.deadlines)
	}
}

func TestEventStreamNestedSubjectsRemainWorkspaceScoped(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	workspaceA, userA := createServerTestWorkspace(t, database)
	workspaceB, _ := createServerTestWorkspace(t, database)
	imageA := createServerTestItemImage(t, database, workspaceA, userA, "https://source.example/canvas/"+uuid.NewString())
	eventID := "sse-workspace-" + uuid.NewString()
	subject := fmt.Sprintf("item-images/%d/annotations/line-1", imageA.ID)
	body := fmt.Sprintf(`{"specversion":"1.0","id":%q,"type":"dev.scribe.annotation.updated","subject":%q,"data":{}}`, eventID, subject)
	events := store.NewTranscriptionJobStore(database)
	if err := events.EnqueueWebhookEvent(ctx, eventID, "dev.scribe.annotation.updated", subject, body, nil); err != nil {
		t.Fatalf("enqueue workspace event: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DELETE FROM event_outbox WHERE event_id = ?`, eventID)
	})

	workspaceAEvents, err := events.ListEventOutboxAfterForWorkspace(ctx, 0, workspaceA, 1000)
	if err != nil {
		t.Fatalf("list workspace A events: %v", err)
	}
	workspaceBEvents, err := events.ListEventOutboxAfterForWorkspace(ctx, 0, workspaceB, 1000)
	if err != nil {
		t.Fatalf("list workspace B events: %v", err)
	}
	findEvent := func(records []store.EventOutboxRecord) (store.EventOutboxRecord, bool) {
		for _, record := range records {
			if record.EventID == eventID {
				return record, true
			}
		}
		return store.EventOutboxRecord{}, false
	}
	record, visibleToAQuery := findEvent(workspaceAEvents)
	if !visibleToAQuery {
		t.Fatal("nested event was absent from its owning workspace SSE query")
	}
	if _, visibleToBQuery := findEvent(workspaceBEvents); visibleToBQuery {
		t.Fatal("nested event crossed the workspace SSE query boundary")
	}

	evt, err := cloudEventFromOutbox(record.EventID, record.EventType, record.Subject, record.BodyJSON)
	if err != nil {
		t.Fatalf("decode workspace event: %v", err)
	}
	handler := &Handler{items: store.NewItemStore(database)}
	if !handler.eventVisibleToWorkspaceCached(ctx, workspaceA, evt, newEventVisibilityCache()) {
		t.Fatal("nested event failed the owning workspace SSE visibility check")
	}
	if handler.eventVisibleToWorkspaceCached(ctx, workspaceB, evt, newEventVisibilityCache()) {
		t.Fatal("nested event crossed the defense-in-depth SSE visibility check")
	}
}
