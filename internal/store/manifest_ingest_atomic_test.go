package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestManifestIngestRollbackAndExpiredLeaseRetryAreAtomic(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, database)
	items := store.NewItemStore(database)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	limits := storageQuotaTestLimits()
	suffix := uuid.NewString()
	authoritativeContext, err := store.NewContextStore(database).Create(ctx, store.Context{
		UserID: &userID, WorkspaceID: &workspaceID, Name: "manifest-context-" + suffix,
		SegmentationModel: "tesseract", TranscriptionProvider: "tesseract", TranscriptionModel: "eng",
	})
	if err != nil {
		t.Fatalf("create manifest context: %v", err)
	}
	forgedContext := authoritativeContext
	forgedContext.Name = "caller-forged-manifest"
	forgedContext.TranscriptionModel = "forged-model"
	key := "manifest-crash-" + suffix
	requestHash := fmt.Sprintf("%064x", 71)
	reservedRequest, created, err := jobs.ReserveExternalRequest(ctx, workspaceID, "item-create", key, requestHash, "")
	if err != nil || !created {
		t.Fatalf("ReserveExternalRequest = %+v/%t/%v", reservedRequest, created, err)
	}
	quota, err := items.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Items: 1, Images: 2}, limits)
	if err != nil {
		t.Fatalf("ReserveStorageQuota: %v", err)
	}
	pageFactory := func(itemImageID uint64, canvasURI string) (store.AnnotationPage, error) {
		payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
			PublicBaseURL: "https://scribe.example", ItemImageID: itemImageID, CanvasURI: canvasURI,
		}, []any{})
		if err != nil {
			return store.AnnotationPage{}, err
		}
		pageID, err := iiif.CanonicalPageID("https://scribe.example", itemImageID)
		if err != nil {
			return store.AnnotationPage{}, err
		}
		return store.AnnotationPage{PageID: pageID, CanvasURI: canvasURI, Payload: string(payload)}, nil
	}
	itemID := "item_manifest_atomic_" + suffix
	sourceManifest := `{"@context":"http://iiif.io/api/presentation/3/context.json"}`
	baseCommit := store.ManifestIngestCommit{
		Item: dbstore.CreateItemParams{
			ID: itemID, UserID: userID, WorkspaceID: workspaceID, Name: "Atomic manifest",
			SourceType: "manifest", SourceURL: "https://source.example/manifest/" + suffix, SourceManifest: sourceManifest,
		},
		Canvases: []store.ManifestIngestCanvas{
			{Image: dbstore.CreateItemImageParams{Sequence: 0, ImageURL: "https://images.example/1.jpg", CanvasURI: "https://source.example/canvas/1"}, BuildPage: pageFactory, EnqueueJob: true},
			{Image: dbstore.CreateItemImageParams{Sequence: 1, ImageURL: "https://images.example/2.jpg", CanvasURI: "https://source.example/canvas/2"}, BuildPage: func(uint64, string) (store.AnnotationPage, error) {
				return store.AnnotationPage{}, fmt.Errorf("simulated process termination boundary")
			}, EnqueueJob: true},
		},
		PublicBaseURL: "https://scribe.example", TranscriptionContext: &forgedContext, Reservation: quota, Limits: limits,
		ExternalRequest: &store.SingleFileIngestExternalRequest{
			Source: "item-create", IdempotencyKey: key, LeaseOwner: reservedRequest.LeaseOwner,
		},
	}
	if _, err := annotations.CommitManifestIngest(ctx, baseCommit); err == nil {
		t.Fatal("CommitManifestIngest unexpectedly succeeded")
	}
	for table := range map[string]struct{}{"items": {}, "item_images": {}, "annotation_pages": {}, "transcription_jobs": {}} {
		var count int
		query := "SELECT COUNT(*) FROM " + table + " WHERE "
		switch table {
		case "items":
			query += "id = ?"
		case "item_images":
			query += "item_id = ?"
		case "annotation_pages":
			query += "item_image_id IN (SELECT id FROM item_images WHERE item_id = ?)"
		default:
			query += "item_image_id IN (SELECT id FROM item_images WHERE item_id = ?)"
		}
		if err := database.QueryRowContext(ctx, query, itemID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial %s rows = %d/%v, want 0", table, count, err)
		}
	}
	if err := items.ReleaseStorageQuotaReservation(ctx, quota); err != nil {
		t.Fatalf("release rolled-back quota reservation: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE external_requests
SET lease_until = DATE_SUB(NOW(), INTERVAL 1 MINUTE)
WHERE workspace_id = ? AND source = 'item-create' AND idempotency_key = ?
`, workspaceID, key); err != nil {
		t.Fatalf("expire simulated crashed request: %v", err)
	}
	reclaimed, created, err := jobs.ReserveExternalRequest(ctx, workspaceID, "item-create", key, requestHash, "")
	if err != nil || !created || reclaimed.AttemptCount != 2 {
		t.Fatalf("reclaim expired manifest request = %+v/%t/%v", reclaimed, created, err)
	}
	quota, err = items.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Items: 1, Images: 2}, limits)
	if err != nil {
		t.Fatalf("reserve retry quota: %v", err)
	}
	baseCommit.Reservation = quota
	baseCommit.ExternalRequest.LeaseOwner = reclaimed.LeaseOwner
	baseCommit.Canvases[1].BuildPage = pageFactory
	committed, err := annotations.CommitManifestIngest(ctx, baseCommit)
	if err != nil {
		t.Fatalf("retry CommitManifestIngest: %v", err)
	}
	if committed.Item.ID != itemID || len(committed.Item.Images) != 2 {
		t.Fatalf("committed manifest = %+v", committed.Item)
	}
	if len(committed.TranscriptionJobIDs) != 2 {
		t.Fatalf("manifest transcription jobs = %v, want 2", committed.TranscriptionJobIDs)
	}
	for _, jobID := range committed.TranscriptionJobIDs {
		job, loadErr := jobs.Get(ctx, jobID)
		if loadErr != nil {
			t.Fatalf("load manifest job %d: %v", jobID, loadErr)
		}
		var snapshottedContext store.Context
		if decodeErr := json.Unmarshal(job.ContextSnapshot, &snapshottedContext); decodeErr != nil {
			t.Fatalf("decode manifest context snapshot: %v", decodeErr)
		}
		if snapshottedContext.Name != authoritativeContext.Name || snapshottedContext.TranscriptionModel != authoritativeContext.TranscriptionModel {
			t.Fatalf("manifest context snapshot = %+v, want authoritative %+v", snapshottedContext, authoritativeContext)
		}
	}
	var materializedBytes, measuredBytes uint64
	if err := database.QueryRowContext(ctx, `SELECT database_bytes FROM storage_quota_usage WHERE workspace_id = ?`, workspaceID).Scan(&materializedBytes); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT CAST(COALESCE(OCTET_LENGTH(i.source_manifest), 0) + COALESCE((
  SELECT SUM(OCTET_LENGTH(ap.payload)) FROM annotation_pages ap
  JOIN item_images ii ON ii.id = ap.item_image_id WHERE ii.item_id = i.id
), 0) AS UNSIGNED)
FROM items i WHERE i.id = ?`, itemID).Scan(&measuredBytes); err != nil {
		t.Fatal(err)
	}
	if measuredBytes < uint64(len(sourceManifest)) || materializedBytes != measuredBytes {
		t.Fatalf("manifest durable bytes = materialized %d / measured %d / source %d", materializedBytes, measuredBytes, len(sourceManifest))
	}
	if err := items.RebuildStorageQuotaUsage(ctx); err != nil {
		t.Fatalf("RebuildStorageQuotaUsage: %v", err)
	}
	var rebuiltBytes uint64
	if err := database.QueryRowContext(ctx, `SELECT database_bytes FROM storage_quota_usage WHERE workspace_id = ?`, workspaceID).Scan(&rebuiltBytes); err != nil || rebuiltBytes != measuredBytes {
		t.Fatalf("rebuilt manifest bytes = %d/%v, want %d", rebuiltBytes, err, measuredBytes)
	}
	replay, created, err := jobs.ReserveExternalRequest(ctx, workspaceID, "item-create", key, requestHash, "")
	if err != nil || created || replay.Status != store.ExternalRequestStatusCompleted || replay.ItemID != itemID {
		t.Fatalf("completed manifest replay = %+v/%t/%v", replay, created, err)
	}
}

func TestManifestIngestRejectsNonCanonicalPagesAtomically(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, database)
	items := store.NewItemStore(database)
	annotations := store.NewAnnotationStore(database)
	limits := storageQuotaTestLimits()

	tests := []struct {
		name   string
		mutate func(uint64, string, *store.AnnotationPage, map[string]any)
	}{
		{
			name: "wrong page owner",
			mutate: func(itemImageID uint64, _ string, page *store.AnnotationPage, document map[string]any) {
				foreignID, err := iiif.CanonicalPageID("https://scribe.example", itemImageID+1)
				if err != nil {
					t.Fatal(err)
				}
				page.PageID = foreignID
				document["id"] = foreignID
			},
		},
		{
			name: "foreign child",
			mutate: func(_ uint64, _ string, _ *store.AnnotationPage, document map[string]any) {
				items := document["items"].([]any)
				items[0].(map[string]any)["id"] = "https://foreign.example/v1/item-images/1/annotations/items/0123456789abcdef0123456789abcdef"
			},
		},
		{
			name: "duplicate child",
			mutate: func(_ uint64, _ string, _ *store.AnnotationPage, document map[string]any) {
				items := document["items"].([]any)
				document["items"] = append(items, items[0])
			},
		},
		{
			name: "out of bounds geometry",
			mutate: func(_ uint64, canvasURI string, _ *store.AnnotationPage, document map[string]any) {
				document["items"].([]any)[0].(map[string]any)["target"] = canvasURI + "#xywh=39,19,2,2"
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suffix := uuid.NewString()
			itemID := fmt.Sprintf("item_manifest_invalid_%d_%s", index, suffix)
			reservation, err := items.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Items: 1, Images: 1}, limits)
			if err != nil {
				t.Fatalf("ReserveStorageQuota: %v", err)
			}
			factory := func(itemImageID uint64, canvasURI string) (store.AnnotationPage, error) {
				annotation := map[string]any{
					"type": "Annotation", "motivation": "supplementing", "textGranularity": "line",
					"body":   map[string]any{"type": "TextualBody", "purpose": "supplementing", "value": "text"},
					"target": canvasURI + "#xywh=1,2,30,10",
				}
				payload, buildErr := iiif.NewAnnotationPage(iiif.PageIdentity{
					PublicBaseURL: "https://scribe.example", ItemImageID: itemImageID, CanvasURI: canvasURI,
				}, []any{annotation})
				if buildErr != nil {
					return store.AnnotationPage{}, buildErr
				}
				pageID, buildErr := iiif.CanonicalPageID("https://scribe.example", itemImageID)
				if buildErr != nil {
					return store.AnnotationPage{}, buildErr
				}
				page := store.AnnotationPage{PageID: pageID, CanvasURI: canvasURI, Payload: string(payload)}
				var document map[string]any
				if buildErr := json.Unmarshal(payload, &document); buildErr != nil {
					return store.AnnotationPage{}, buildErr
				}
				test.mutate(itemImageID, canvasURI, &page, document)
				invalidPayload, buildErr := json.Marshal(document)
				page.Payload = string(invalidPayload)
				return page, buildErr
			}
			_, err = annotations.CommitManifestIngest(ctx, store.ManifestIngestCommit{
				Item: dbstore.CreateItemParams{ID: itemID, UserID: userID, WorkspaceID: workspaceID, Name: test.name, SourceType: "manifest"},
				Canvases: []store.ManifestIngestCanvas{{
					Image: dbstore.CreateItemImageParams{
						ImageURL: "https://images.example/invalid.jpg", CanvasURI: "https://source.example/canvas/invalid",
						Width: 40, Height: 20,
					},
					BuildPage: factory,
				}},
				PublicBaseURL: "https://scribe.example", Reservation: reservation, Limits: limits,
			})
			if err == nil {
				t.Fatal("CommitManifestIngest unexpectedly accepted a noncanonical page")
			}
			var itemCount, imageCount, pageCount int
			if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE id = ?`, itemID).Scan(&itemCount); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_images WHERE item_id = ?`, itemID).Scan(&imageCount); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_pages WHERE item_image_id IN (SELECT id FROM item_images WHERE item_id = ?)`, itemID).Scan(&pageCount); err != nil {
				t.Fatal(err)
			}
			if itemCount != 0 || imageCount != 0 || pageCount != 0 {
				t.Fatalf("rejected ingest leaked item/image/page rows = %d/%d/%d", itemCount, imageCount, pageCount)
			}
			if err := items.ReleaseStorageQuotaReservation(ctx, reservation); err != nil {
				t.Fatalf("release rolled-back reservation: %v", err)
			}
		})
	}
}

func TestManifestIngestRejectsLocalUploadReferences(t *testing.T) {
	database := annotationTestDB(t)
	userID, workspaceID := createUploadBatchIdentity(t, database)
	itemID := "item_manifest_local_" + uuid.NewString()
	uploadName := immutableUploadTestName(strings.Repeat("a", 64))
	_, err := store.NewAnnotationStore(database).CommitManifestIngest(context.Background(), store.ManifestIngestCommit{
		Item: dbstore.CreateItemParams{
			ID: itemID, UserID: userID, WorkspaceID: workspaceID,
			Name: "local manifest reference", SourceType: "manifest",
		},
		Canvases: []store.ManifestIngestCanvas{{
			Image: dbstore.CreateItemImageParams{
				ImageURL: "/static/uploads/" + uploadName, StorageBytes: 80,
			},
			BuildPage: func(uint64, string) (store.AnnotationPage, error) {
				t.Fatal("local manifest reference reached page construction")
				return store.AnnotationPage{}, nil
			},
		}},
		PublicBaseURL: "https://scribe.example",
		Reservation: store.StorageQuotaReservation{
			ID: uuid.NewString(), WorkspaceID: workspaceID,
		},
		Limits: storageQuotaTestLimits(),
	})
	if err == nil || !strings.Contains(err.Error(), "must be remote resources") {
		t.Fatalf("local manifest reference error = %v", err)
	}
	var itemCount int
	if queryErr := database.QueryRow(`SELECT COUNT(*) FROM items WHERE id = ?`, itemID).Scan(&itemCount); queryErr != nil {
		t.Fatalf("count rejected local manifest item: %v", queryErr)
	}
	if itemCount != 0 {
		t.Fatalf("rejected local manifest item count = %d, want 0", itemCount)
	}
}

func TestSingleFileIngestRejectsNonCanonicalPageAtomically(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, database)
	items := store.NewItemStore(database)
	annotations := store.NewAnnotationStore(database)
	limits := storageQuotaTestLimits()
	tests := []struct {
		name   string
		mutate func(string, map[string]any)
	}{
		{
			name: "foreign child ID",
			mutate: func(_ string, document map[string]any) {
				document["items"].([]any)[0].(map[string]any)["id"] = "https://foreign.example/items/0123456789abcdef0123456789abcdef"
			},
		},
		{
			name: "out of bounds geometry",
			mutate: func(canvasURI string, document map[string]any) {
				document["items"].([]any)[0].(map[string]any)["target"] = canvasURI + "#xywh=39,19,2,2"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reservation, err := items.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Items: 1, Images: 1}, limits)
			if err != nil {
				t.Fatalf("ReserveStorageQuota: %v", err)
			}
			itemID := "item_single_invalid_" + uuid.NewString()
			_, err = annotations.CommitSingleFileIngest(ctx, store.SingleFileIngestCommit{
				Item: dbstore.CreateItemParams{
					ID: itemID, UserID: userID, WorkspaceID: workspaceID, Name: test.name, SourceType: "url",
				},
				Image: dbstore.CreateItemImageParams{
					ImageURL: "https://images.example/single-invalid.jpg", CanvasURI: "https://source.example/canvas/single-invalid",
					Width: 40, Height: 20,
				},
				OCRRun:        store.OCRRun{OriginalHOCR: "<html></html>", OriginalText: "text", Provider: "test", Model: "test"},
				PublicBaseURL: "https://scribe.example",
				Reservation:   reservation,
				Limits:        limits,
				BuildPage: func(itemImageID uint64, canvasURI string) (store.AnnotationPage, error) {
					payload, buildErr := iiif.NewAnnotationPage(iiif.PageIdentity{
						PublicBaseURL: "https://scribe.example", ItemImageID: itemImageID, CanvasURI: canvasURI,
					}, []any{map[string]any{
						"type": "Annotation", "motivation": "supplementing", "textGranularity": "line",
						"body":   map[string]any{"type": "TextualBody", "purpose": "supplementing", "value": "text"},
						"target": canvasURI + "#xywh=1,2,30,10",
					}})
					if buildErr != nil {
						return store.AnnotationPage{}, buildErr
					}
					var document map[string]any
					if buildErr := json.Unmarshal(payload, &document); buildErr != nil {
						return store.AnnotationPage{}, buildErr
					}
					test.mutate(canvasURI, document)
					payload, buildErr = json.Marshal(document)
					pageID, idErr := iiif.CanonicalPageID("https://scribe.example", itemImageID)
					if buildErr != nil {
						return store.AnnotationPage{}, buildErr
					}
					return store.AnnotationPage{PageID: pageID, CanvasURI: canvasURI, Payload: string(payload)}, idErr
				},
			})
			if err == nil {
				t.Fatal("CommitSingleFileIngest unexpectedly accepted invalid canonical state")
			}
			for _, table := range []string{"items", "item_images", "annotation_pages", "ocr_runs"} {
				var count int
				query := "SELECT COUNT(*) FROM " + table
				args := []any{}
				switch table {
				case "items":
					query += " WHERE id = ?"
					args = append(args, itemID)
				case "item_images":
					query += " WHERE item_id = ?"
					args = append(args, itemID)
				default:
					query += " WHERE item_image_id IN (SELECT id FROM item_images WHERE item_id = ?)"
					args = append(args, itemID)
				}
				if err := database.QueryRowContext(ctx, query, args...).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rejected single-file ingest %s rows = %d/%v", table, count, err)
				}
			}
			if err := items.ReleaseStorageQuotaReservation(ctx, reservation); err != nil {
				t.Fatalf("release rolled-back reservation: %v", err)
			}
		})
	}
}

func TestSingleFileIngestSnapshotsLockedAuthoritativeContext(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, database)
	contexts := store.NewContextStore(database)
	authoritative, err := contexts.Create(ctx, store.Context{
		UserID: &userID, WorkspaceID: &workspaceID, Name: "single-context-" + uuid.NewString(),
		SegmentationModel: "tesseract", TranscriptionProvider: "tesseract", TranscriptionModel: "eng",
	})
	if err != nil {
		t.Fatal(err)
	}
	forged := authoritative
	forged.Name = "caller-forged-single"
	forged.TranscriptionModel = "forged-model"
	items := store.NewItemStore(database)
	limits := storageQuotaTestLimits()
	reservation, err := items.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Items: 1, Images: 1, DurableBytes: 4096}, limits)
	if err != nil {
		t.Fatal(err)
	}
	itemID := "item_single_context_" + uuid.NewString()
	result, err := store.NewAnnotationStore(database).CommitSingleFileIngest(ctx, store.SingleFileIngestCommit{
		Item:          dbstore.CreateItemParams{ID: itemID, UserID: userID, WorkspaceID: workspaceID, Name: "single context", SourceType: "url"},
		Image:         dbstore.CreateItemImageParams{ImageURL: "https://images.example/single-context.jpg", CanvasURI: "https://source.example/canvas/single-context", Width: 40, Height: 20},
		OCRRun:        store.OCRRun{SessionID: "single-context-run-" + uuid.NewString(), OriginalHOCR: "<html></html>", OriginalText: "text", Provider: "test", Model: "test", ContextID: &authoritative.ID},
		PublicBaseURL: "https://scribe.example", TranscriptionContext: &forged,
		Reservation: reservation, Limits: limits,
		BuildPage: func(itemImageID uint64, canvasURI string) (store.AnnotationPage, error) {
			payload, buildErr := iiif.NewAnnotationPage(iiif.PageIdentity{
				PublicBaseURL: "https://scribe.example", ItemImageID: itemImageID, CanvasURI: canvasURI,
			}, []any{})
			if buildErr != nil {
				return store.AnnotationPage{}, buildErr
			}
			pageID, buildErr := iiif.CanonicalPageID("https://scribe.example", itemImageID)
			return store.AnnotationPage{PageID: pageID, CanvasURI: canvasURI, Payload: string(payload)}, buildErr
		},
	})
	if err != nil {
		t.Fatalf("commit single-file context ingest: %v", err)
	}
	job, err := store.NewTranscriptionJobStore(database).Get(ctx, result.TranscriptionJobID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot store.Context
	if err := json.Unmarshal(job.ContextSnapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Name != authoritative.Name || snapshot.TranscriptionModel != authoritative.TranscriptionModel {
		t.Fatalf("single-file context snapshot = %+v, want authoritative %+v", snapshot, authoritative)
	}
}
