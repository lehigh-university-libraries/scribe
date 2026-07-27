package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestAnnotationPublicationIsRevisionCheckedTenantScopedAndDurable(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/publication-" + suffix
	workspaceA, imageA := createAnnotationTestResource(t, database, suffix+"-publish-a", canvasURI)
	workspaceB, _ := createAnnotationTestResource(t, database, suffix+"-publish-b", canvasURI)
	createWebhookTestSubscription(t, database, workspaceA)
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE kind = 'triplet_presentation_image' AND resource_key = ?`, fmt.Sprint(imageA))
	})
	annotationStore := store.NewAnnotationStore(database)
	itemStore := store.NewItemStore(database)
	imageRecord, err := itemStore.GetImageForWorkspace(ctx, imageA, workspaceA)
	if err != nil {
		t.Fatalf("load publication image: %v", err)
	}
	const externalReferenceID = "islandora:publication-123"
	const callerIdempotencyKey = "publication-caller-request"
	if _, err := database.ExecContext(ctx, `
UPDATE items
SET metadata = ?, external_reference_id = ?, caller_idempotency_key = ?
WHERE id = ? AND workspace_id = ?`, `{"repository":"islandora","collection":"newspapers"}`, externalReferenceID, callerIdempotencyKey, imageRecord.ItemID, workspaceA); err != nil {
		t.Fatalf("set publication event correlation data: %v", err)
	}
	if public, err := annotationStore.ImageURLIsPublished(ctx, imageRecord.ImageURL); err != nil || public {
		t.Fatalf("ImageURLIsPublished before publication = %t, %v; want false", public, err)
	}

	draft, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceA, imageA, canvasURI, "first published text"), 0)
	if err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	if _, err := annotationStore.LoadPublishedPage(ctx, imageA); !errors.Is(err, store.ErrAnnotationPageNotFound) {
		t.Fatalf("LoadPublishedPage before publish error = %v, want ErrAnnotationPageNotFound", err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceB, imageA, store.AnnotationPublicationOptions{
		ExpectedRevision: draft.Revision,
	}); !errors.Is(err, store.ErrAnnotationPageNotFound) {
		t.Fatalf("cross-workspace PublishPage error = %v, want ErrAnnotationPageNotFound", err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceA, imageA, store.AnnotationPublicationOptions{
		ExpectedRevision: draft.Revision + 1,
	}); !errors.Is(err, store.ErrAnnotationRevisionConflict) {
		t.Fatalf("future-revision PublishPage error = %v, want ErrAnnotationRevisionConflict", err)
	}

	eventID := "publication-" + suffix
	published, err := annotationStore.PublishPage(ctx, workspaceA, imageA, store.AnnotationPublicationOptions{
		ExpectedRevision: draft.Revision,
		EventID:          eventID,
		EventType:        "dev.scribe.annotations.published",
		Subject:          fmt.Sprintf("/item-images/%d", imageA),
	})
	if err != nil {
		t.Fatalf("PublishPage: %v", err)
	}
	if !published.NewlyPublished || published.PublishedRevision != draft.Revision || published.WorkspaceID != workspaceA {
		t.Fatalf("published page = %+v", published)
	}
	if public, err := annotationStore.ImageURLIsPublished(ctx, imageRecord.ImageURL); err != nil || !public {
		t.Fatalf("ImageURLIsPublished after publication = %t, %v; want true", public, err)
	}
	if err := iiif.ValidateAnnotationPage([]byte(published.Payload)); err != nil {
		t.Fatalf("published page failed libops validation: %v", err)
	}
	assertPageText(t, published.Payload, "first published text")

	loaded, err := annotationStore.LoadPublishedPage(ctx, imageA)
	if err != nil {
		t.Fatalf("LoadPublishedPage: %v", err)
	}
	if loaded.PublishedRevision != draft.Revision || loaded.NewlyPublished {
		t.Fatalf("loaded publication = %+v", loaded)
	}
	assertPageText(t, loaded.Payload, "first published text")

	// Defense in depth: even if malformed data bypasses the application and
	// associates the globally unique image with a second workspace, publication
	// must fail closed instead of ON DUPLICATE updating the real owner's row.
	rogue := canonicalTestPage(t, workspaceB, imageA, canvasURI, "cross-tenant overwrite")
	rogue.PageID += "/rogue"
	var rogueDocument map[string]any
	if err := json.Unmarshal([]byte(rogue.Payload), &rogueDocument); err != nil {
		t.Fatalf("decode rogue page: %v", err)
	}
	rogueDocument["id"] = rogue.PageID
	roguePayload, _ := json.Marshal(rogueDocument)
	if _, err := database.ExecContext(ctx, `
INSERT INTO annotation_pages (workspace_id, item_image_id, page_id, canvas_uri, payload, revision)
VALUES (?, ?, ?, ?, ?, 1)`, workspaceB, imageA, rogue.PageID, canvasURI, string(roguePayload)); err != nil {
		t.Fatalf("inject cross-tenant audit fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM annotation_pages WHERE workspace_id = ? AND item_image_id = ?`, workspaceB, imageA)
	})
	if violations, err := store.AuditRelationshipIntegrity(ctx, database); err != nil {
		t.Fatalf("audit cross-tenant canonical row: %v", err)
	} else if !relationshipViolationContains(violations, "annotation_pages.image", 1) {
		t.Fatalf("cross-tenant canonical row was not audited: %+v", violations)
	}
	rogueEventID := "publication-rogue-" + suffix
	if _, err := annotationStore.PublishPage(ctx, workspaceB, imageA, store.AnnotationPublicationOptions{
		ExpectedRevision: 1,
		EventID:          rogueEventID,
	}); err == nil {
		t.Fatal("malformed cross-tenant publication overwrote the globally unique public resource")
	}
	loaded, err = annotationStore.LoadPublishedPage(ctx, imageA)
	if err != nil {
		t.Fatalf("LoadPublishedPage after rejected cross-tenant publication: %v", err)
	}
	if loaded.WorkspaceID != workspaceA {
		t.Fatalf("public publication owner = %d, want %d", loaded.WorkspaceID, workspaceA)
	}
	assertPageText(t, loaded.Payload, "first published text")
	var rogueEventCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, rogueEventID).Scan(&rogueEventCount); err != nil {
		t.Fatalf("count rejected cross-tenant event: %v", err)
	}
	if rogueEventCount != 0 {
		t.Fatalf("rejected cross-tenant publication emitted %d events, want 0", rogueEventCount)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM annotation_pages WHERE workspace_id = ? AND item_image_id = ?`, workspaceB, imageA); err != nil {
		t.Fatalf("repair cross-tenant canonical audit fixture: %v", err)
	}

	var mirrorRevision uint64
	var mirrorPayload, mirrorStatus string
	if err := database.QueryRowContext(ctx, `
SELECT revision, payload, status
FROM annotation_mirror_outbox
WHERE item_image_id = ?`, imageA).Scan(&mirrorRevision, &mirrorPayload, &mirrorStatus); err != nil {
		t.Fatalf("load publication mirror outbox: %v", err)
	}
	if mirrorRevision != draft.Revision || mirrorStatus != "pending" {
		t.Fatalf("mirror revision/status = %d/%s, want %d/pending", mirrorRevision, mirrorStatus, draft.Revision)
	}
	assertPageText(t, mirrorPayload, "first published text")

	var eventWorkspaceID uint64
	var eventType, eventBody string
	if err := database.QueryRowContext(ctx, `
SELECT workspace_id, event_type, body_json
FROM event_outbox
WHERE event_id = ?`, eventID).Scan(&eventWorkspaceID, &eventType, &eventBody); err != nil {
		t.Fatalf("load publication event: %v", err)
	}
	if eventWorkspaceID != workspaceA || eventType != "dev.scribe.annotations.published" {
		t.Fatalf("publication event workspace/type = %d/%q", eventWorkspaceID, eventType)
	}
	var eventDocument struct {
		Time string `json:"time"`
		Data struct {
			AnnotationPageJSON  string         `json:"annotationPageJson"`
			AnnotationPageID    string         `json:"annotationPageId"`
			WorkspaceID         uint64         `json:"workspaceId"`
			ItemID              string         `json:"itemId"`
			ItemImageID         uint64         `json:"itemImageId"`
			CanvasURI           string         `json:"canvasUri"`
			Revision            uint64         `json:"revision"`
			Metadata            map[string]any `json:"metadata"`
			IdempotencyKey      string         `json:"idempotencyKey"`
			ExternalReferenceID string         `json:"externalReferenceId"`
			PublishedAt         string         `json:"publishedAt"`
			PublishedRevision   uint64         `json:"publishedRevision"`
			PublicURL           string         `json:"publicUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(eventBody), &eventDocument); err != nil {
		t.Fatalf("decode publication event: %v", err)
	}
	if eventDocument.Data.PublishedRevision != draft.Revision || eventDocument.Data.PublicURL != draft.PageID || eventDocument.Data.AnnotationPageID != draft.PageID || eventDocument.Data.AnnotationPageJSON != "" {
		t.Fatalf("publication event data = %+v", eventDocument.Data)
	}
	if eventDocument.Data.WorkspaceID != workspaceA || eventDocument.Data.ItemID != imageRecord.ItemID || eventDocument.Data.ItemImageID != imageA || eventDocument.Data.CanvasURI != canvasURI || eventDocument.Data.Revision != draft.Revision {
		t.Fatalf("publication event resource data = %+v", eventDocument.Data)
	}
	if eventDocument.Data.Metadata["repository"] != "islandora" || eventDocument.Data.Metadata["collection"] != "newspapers" || eventDocument.Data.IdempotencyKey != callerIdempotencyKey || eventDocument.Data.ExternalReferenceID != externalReferenceID {
		t.Fatalf("publication event correlation data = %+v", eventDocument.Data)
	}
	if len(eventBody) >= 4096 {
		t.Fatalf("publication event body is %d bytes; payload must remain independent of canonical page size", len(eventBody))
	}
	wantPublishedAt := published.PublishedAt.UTC().Format(time.RFC3339Nano)
	if eventDocument.Time != wantPublishedAt || eventDocument.Data.PublishedAt != wantPublishedAt {
		t.Fatalf("publication timestamps = event %q data %q, want %q", eventDocument.Time, eventDocument.Data.PublishedAt, wantPublishedAt)
	}
	var deliveryCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE event_id = ?`, eventID).Scan(&deliveryCount); err != nil {
		t.Fatalf("count publication webhook: %v", err)
	}
	if deliveryCount != 1 {
		t.Fatalf("publication webhook count = %d, want 1", deliveryCount)
	}

	idempotentEventID := "publication-idempotent-" + suffix
	idempotent, err := annotationStore.PublishPage(ctx, workspaceA, imageA, store.AnnotationPublicationOptions{
		ExpectedRevision: draft.Revision,
		EventID:          idempotentEventID,
	})
	if err != nil {
		t.Fatalf("idempotent PublishPage: %v", err)
	}
	if idempotent.NewlyPublished {
		t.Fatal("idempotent publication reported a newly published revision")
	}
	var idempotentEventCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, idempotentEventID).Scan(&idempotentEventCount); err != nil {
		t.Fatalf("count idempotent publication event: %v", err)
	}
	if idempotentEventCount != 0 {
		t.Fatalf("idempotent publication emitted %d events, want 0", idempotentEventCount)
	}

	draft.Payload = replacePageText(t, draft.Payload, "unpublished second draft")
	secondDraft, err := annotationStore.SavePage(ctx, draft, draft.Revision)
	if err != nil {
		t.Fatalf("SavePage second draft: %v", err)
	}
	stillPublished, err := annotationStore.LoadPublishedPage(ctx, imageA)
	if err != nil {
		t.Fatalf("LoadPublishedPage after draft save: %v", err)
	}
	if stillPublished.PublishedRevision != draft.Revision {
		t.Fatalf("published revision after draft save = %d, want %d", stillPublished.PublishedRevision, draft.Revision)
	}
	assertPageText(t, stillPublished.Payload, "first published text")
	if _, err := annotationStore.PublishPage(ctx, workspaceA, imageA, store.AnnotationPublicationOptions{
		ExpectedRevision: draft.Revision,
	}); !errors.Is(err, store.ErrAnnotationRevisionConflict) {
		t.Fatalf("stale PublishPage error = %v, want ErrAnnotationRevisionConflict", err)
	}
	secondPublished, err := annotationStore.PublishPage(ctx, workspaceA, imageA, store.AnnotationPublicationOptions{
		ExpectedRevision: secondDraft.Revision,
		EventID:          "publication-second-" + suffix,
	})
	if err != nil {
		t.Fatalf("PublishPage second revision: %v", err)
	}
	if secondPublished.PublishedRevision != secondDraft.Revision {
		t.Fatalf("second published revision = %d, want %d", secondPublished.PublishedRevision, secondDraft.Revision)
	}
	assertPageText(t, secondPublished.Payload, "unpublished second draft")

	if err := itemStore.DeleteItemImageForWorkspace(ctx, imageA, workspaceA); err != nil {
		t.Fatalf("DeleteItemImageForWorkspace: %v", err)
	}
	if _, err := annotationStore.LoadPublishedPage(ctx, imageA); !errors.Is(err, store.ErrAnnotationPageNotFound) {
		t.Fatalf("LoadPublishedPage after delete error = %v, want ErrAnnotationPageNotFound", err)
	}
	if public, err := annotationStore.ImageURLIsPublished(ctx, imageRecord.ImageURL); err != nil || public {
		t.Fatalf("ImageURLIsPublished after delete = %t, %v; want false", public, err)
	}
	for table, query := range map[string]string{
		"published snapshot": `SELECT COUNT(*) FROM published_annotation_pages WHERE item_image_id = ?`,
		"mirror outbox":      `SELECT COUNT(*) FROM annotation_mirror_outbox WHERE item_image_id = ?`,
	} {
		var count int
		if err := database.QueryRowContext(ctx, query, imageA).Scan(&count); err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("count %s after delete: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s count after delete = %d, want 0", table, count)
		}
	}
}

func TestAnnotationMirrorCoalescingSerializesRemoteRevisionWrites(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/mirror-order-" + suffix
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-mirror-order", canvasURI)
	annotationStore := store.NewAnnotationStore(database)

	draft, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "revision one"), 0)
	if err != nil {
		t.Fatalf("save first mirror revision: %v", err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceID, imageID, store.AnnotationPublicationOptions{ExpectedRevision: draft.Revision}); err != nil {
		t.Fatalf("publish first mirror revision: %v", err)
	}
	oldDelivery, err := annotationStore.ClaimAnnotationMirror(ctx, 5*time.Minute)
	if err != nil || oldDelivery == nil || oldDelivery.Revision != draft.Revision {
		t.Fatalf("claim first mirror revision = %+v/%v", oldDelivery, err)
	}
	var oldLeaseUntil time.Time
	if err := database.QueryRowContext(ctx, `
SELECT lease_until
FROM annotation_mirror_outbox
WHERE item_image_id = ?`, imageID).Scan(&oldLeaseUntil); err != nil {
		t.Fatalf("load first mirror lease: %v", err)
	}

	draft.Payload = replacePageText(t, draft.Payload, "revision two")
	secondDraft, err := annotationStore.SavePage(ctx, draft, draft.Revision)
	if err != nil {
		t.Fatalf("save second mirror revision: %v", err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceID, imageID, store.AnnotationPublicationOptions{ExpectedRevision: secondDraft.Revision}); err != nil {
		t.Fatalf("publish second mirror revision: %v", err)
	}

	var revision uint64
	var payload, status string
	var nextAttemptAt time.Time
	var leaseUntil sql.NullTime
	var lockedBy sql.NullString
	if err := database.QueryRowContext(ctx, `
SELECT revision, payload, status, next_attempt_at, lease_until, locked_by
FROM annotation_mirror_outbox
WHERE item_image_id = ?`, imageID).Scan(&revision, &payload, &status, &nextAttemptAt, &leaseUntil, &lockedBy); err != nil {
		t.Fatalf("load coalesced mirror revision: %v", err)
	}
	if revision != secondDraft.Revision || status != "pending" || leaseUntil.Valid || lockedBy.Valid {
		t.Fatalf("coalesced mirror state = revision %d status %s lease %v owner %v", revision, status, leaseUntil, lockedBy)
	}
	assertPageText(t, payload, "revision two")
	if nextAttemptAt.Before(oldLeaseUntil) {
		t.Fatalf("replacement next attempt %s precedes old lease horizon %s", nextAttemptAt, oldLeaseUntil)
	}
	if overlapping, err := annotationStore.ClaimAnnotationMirror(ctx, time.Minute); err != nil || overlapping != nil {
		t.Fatalf("replacement mirror overlapped first PUT = %+v/%v", overlapping, err)
	}

	if err := annotationStore.CompleteAnnotationMirror(ctx, *oldDelivery); !errors.Is(err, store.ErrAnnotationMirrorLease) {
		t.Fatalf("stale mirror success error = %v, want ErrAnnotationMirrorLease", err)
	}
	if err := annotationStore.RetryAnnotationMirror(ctx, *oldDelivery, errors.New("stale PUT failed"), time.Now()); !errors.Is(err, store.ErrAnnotationMirrorLease) {
		t.Fatalf("stale mirror failure error = %v, want ErrAnnotationMirrorLease", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT revision, payload, status, next_attempt_at
FROM annotation_mirror_outbox
WHERE item_image_id = ?`, imageID).Scan(&revision, &payload, &status, &nextAttemptAt); err != nil {
		t.Fatalf("reload mirror after stale workers: %v", err)
	}
	if revision != secondDraft.Revision || status != "pending" {
		t.Fatalf("stale worker changed replacement mirror = revision %d status %s", revision, status)
	}
	assertPageText(t, payload, "revision two")

	// Advance the database clock boundary deterministically instead of making
	// the test wait for the production lease duration.
	if _, err := database.ExecContext(ctx, `
UPDATE annotation_mirror_outbox
SET next_attempt_at = DATE_SUB(NOW(), INTERVAL 1 SECOND)
WHERE item_image_id = ?`, imageID); err != nil {
		t.Fatalf("advance replacement mirror horizon: %v", err)
	}
	newDelivery, err := annotationStore.ClaimAnnotationMirror(ctx, time.Minute)
	if err != nil || newDelivery == nil {
		t.Fatalf("claim replacement mirror after horizon = %+v/%v", newDelivery, err)
	}
	if newDelivery.Revision != secondDraft.Revision {
		t.Fatalf("replacement delivery revision = %d, want %d", newDelivery.Revision, secondDraft.Revision)
	}
	assertPageText(t, newDelivery.Payload, "revision two")
	if err := annotationStore.CompleteAnnotationMirror(ctx, *newDelivery); err != nil {
		t.Fatalf("complete replacement mirror: %v", err)
	}
}

func TestAnnotationMirrorFailurePersistsOnlyBoundedCategory(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/mirror-redaction-" + suffix
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-mirror-redaction", canvasURI)
	annotationStore := store.NewAnnotationStore(database)

	draft, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "redact mirror failure"), 0)
	if err != nil {
		t.Fatalf("save mirror page: %v", err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceID, imageID, store.AnnotationPublicationOptions{ExpectedRevision: draft.Revision}); err != nil {
		t.Fatalf("publish mirror page: %v", err)
	}
	delivery, err := annotationStore.ClaimAnnotationMirror(ctx, time.Minute)
	if err != nil || delivery == nil {
		t.Fatalf("claim annotation mirror = %+v/%v", delivery, err)
	}

	untrustedFailure := strings.Repeat(
		"Triplet upstream returned 401 unauthorized; "+
			"url=https://user:TOKEN-SENTINEL@triplet.example/path?api_key=QUERY-SENTINEL; "+
			"response_body=BODY-SENTINEL; SQL=SELECT * FROM secrets; Vault=VAULT-SENTINEL; ",
		32,
	)
	if err := annotationStore.RetryAnnotationMirror(ctx, *delivery, errors.New(untrustedFailure), time.Now()); err != nil {
		t.Fatalf("retry annotation mirror: %v", err)
	}
	var lastError string
	if err := database.QueryRowContext(ctx, `
SELECT last_error
FROM annotation_mirror_outbox
WHERE item_image_id = ?`, imageID).Scan(&lastError); err != nil {
		t.Fatalf("load annotation mirror failure: %v", err)
	}
	assertBoundedFailureCategory(t, lastError, "annotation mirror authorization failed")
}
