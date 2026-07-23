package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestProviderCallAuditsAreMetadataOnlyScopedAndQuotaAccounted(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceID, imageID := createAnnotationTestResource(
		t, database, suffix+"-provider-audit", "https://source.example/canvas/"+suffix,
	)
	var itemID string
	if err := database.QueryRow(`SELECT item_id FROM item_images WHERE id = ?`, imageID).Scan(&itemID); err != nil {
		t.Fatalf("load provider audit item: %v", err)
	}

	var capturedBodyColumns int
	if err := database.QueryRow(`
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'provider_call_audits'
  AND column_name IN ('prompt', 'request_json', 'response_json')`).Scan(&capturedBodyColumns); err != nil {
		t.Fatalf("inspect provider audit schema: %v", err)
	}
	if capturedBodyColumns != 0 {
		t.Fatalf("provider audit captured-body columns = %d, want 0", capturedBodyColumns)
	}

	for _, test := range []struct {
		name          string
		provider      string
		operation     string
		databaseBytes uint64
		httpStatus    any
	}{
		{name: "blank provider", provider: " ", operation: "transcribe", databaseBytes: 512},
		{name: "blank operation", provider: "gemini", operation: " ", databaseBytes: 512},
		{name: "under-accounted", provider: "gemini", operation: "transcribe", databaseBytes: 511},
		{name: "invalid status", provider: "gemini", operation: "transcribe", databaseBytes: 512, httpStatus: 99},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := database.Exec(`
INSERT INTO provider_call_audits (
  workspace_id, provider, model, operation, http_status, duration_ms, database_bytes
) VALUES (?, ?, 'model', ?, ?, 1, ?)`, workspaceID, test.provider, test.operation, test.httpStatus, test.databaseBytes)
			if err == nil {
				t.Fatal("invalid provider audit bypassed schema CHECK")
			}
		})
	}

	itemStore := store.NewItemStore(database)
	workspaceBefore, err := itemStore.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load workspace quota before audit: %v", err)
	}
	globalBefore, err := itemStore.GetStorageQuotaUsage(ctx, 0)
	if err != nil {
		t.Fatalf("load global quota before audit: %v", err)
	}
	limits := storageQuotaTestLimits()
	limits.MaxBytesPerWorkspace = workspaceBefore.UploadBlobBytes + workspaceBefore.DatabaseBytes + 512
	limits.MaxBytesTotal = globalBefore.UploadBlobBytes + globalBefore.DatabaseBytes + 512
	audits := store.NewProviderCallAuditStore(database)
	if err := audits.SetStorageQuotaLimits(limits); err != nil {
		t.Fatalf("set provider audit quota: %v", err)
	}
	status := 503
	if err := audits.Create(ctx, store.ProviderCallAudit{
		WorkspaceID: workspaceID, SessionID: "audit-" + suffix, ItemImageID: &imageID,
		Provider: "gemini", Model: "gemini-test", Operation: "transcribe",
		ErrorMessage: "provider request failed", HTTPStatus: &status, DurationMS: 42,
	}); err != nil {
		t.Fatalf("create provider call audit: %v", err)
	}

	var databaseBytes uint64
	if err := database.QueryRow(`
SELECT database_bytes
FROM provider_call_audits
WHERE workspace_id = ? AND session_id = ?`, workspaceID, "audit-"+suffix).Scan(&databaseBytes); err != nil {
		t.Fatalf("load provider audit accounting: %v", err)
	}
	if databaseBytes != 512 {
		t.Fatalf("provider audit database bytes = %d, want minimum charge 512", databaseBytes)
	}
	workspaceAfterCreate, err := itemStore.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load workspace quota after audit: %v", err)
	}
	globalAfterCreate, err := itemStore.GetStorageQuotaUsage(ctx, 0)
	if err != nil {
		t.Fatalf("load global quota after audit: %v", err)
	}
	if workspaceAfterCreate.DatabaseBytes != workspaceBefore.DatabaseBytes+databaseBytes ||
		globalAfterCreate.DatabaseBytes != globalBefore.DatabaseBytes+databaseBytes {
		t.Fatalf("provider audit quota delta = workspace %d->%d, global %d->%d, want +%d",
			workspaceBefore.DatabaseBytes, workspaceAfterCreate.DatabaseBytes,
			globalBefore.DatabaseBytes, globalAfterCreate.DatabaseBytes, databaseBytes)
	}
	if err := audits.Create(ctx, store.ProviderCallAudit{
		WorkspaceID: workspaceID, Provider: "gemini", Model: "gemini-test", Operation: "transcribe",
	}); !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("provider audit beyond quota error = %v, want ErrStorageQuotaExceeded", err)
	}
	var auditCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM provider_call_audits WHERE workspace_id = ?`, workspaceID).Scan(&auditCount); err != nil {
		t.Fatalf("count provider audits after rejected create: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("provider audit count after rejected create = %d, want 1", auditCount)
	}

	listed, err := audits.ListByItem(ctx, workspaceID, itemID, 10)
	if err != nil || len(listed) != 1 || listed[0].WorkspaceID != workspaceID || listed[0].ItemImageID == nil || *listed[0].ItemImageID != imageID {
		t.Fatalf("workspace audit list = %#v, %v", listed, err)
	}
	crossWorkspace, err := audits.ListByItem(ctx, store.AnonymousWorkspaceID, itemID, 10)
	if err != nil || len(crossWorkspace) != 0 {
		t.Fatalf("cross-workspace audit list = %#v, %v; want empty", crossWorkspace, err)
	}

	if err := itemStore.DeleteItemImageForWorkspace(ctx, imageID, workspaceID); err != nil {
		t.Fatalf("delete audited item image: %v", err)
	}
	workspaceAfterDelete, err := itemStore.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load workspace quota after audited image deletion: %v", err)
	}
	if workspaceAfterDelete.DatabaseBytes != workspaceBefore.DatabaseBytes {
		t.Fatalf("database bytes after audited image deletion = %d, want %d", workspaceAfterDelete.DatabaseBytes, workspaceBefore.DatabaseBytes)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM provider_call_audits WHERE workspace_id = ?`, workspaceID).Scan(&auditCount); err != nil {
		t.Fatalf("count cascaded provider audits: %v", err)
	}
	if auditCount != 0 {
		t.Fatalf("provider audits after image deletion = %d, want 0", auditCount)
	}

	// Metadata-only calls made before an item image exists are retained by
	// workspace and omitted from the explicitly per-item RPC. Retention releases
	// the same persisted accounting that Create admitted.
	unboundSession := "unbound-" + suffix
	if err := audits.Create(ctx, store.ProviderCallAudit{
		WorkspaceID: workspaceID, SessionID: unboundSession,
		Provider: "tesseract", Model: "tesseract", Operation: "segment", DurationMS: 3,
	}); err != nil {
		t.Fatalf("create unbound provider audit: %v", err)
	}
	if _, err := database.Exec(`UPDATE provider_call_audits SET created_at = '2000-01-01 00:00:00' WHERE workspace_id = ? AND session_id = ?`, workspaceID, unboundSession); err != nil {
		t.Fatalf("age unbound provider audit: %v", err)
	}
	if err := audits.Retain(ctx, time.Hour); err != nil {
		t.Fatalf("retain unbound provider audit: %v", err)
	}
	workspaceAfterRetention, err := itemStore.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load workspace quota after audit retention: %v", err)
	}
	if workspaceAfterRetention.DatabaseBytes != workspaceBefore.DatabaseBytes {
		t.Fatalf("database bytes after audit retention = %d, want %d", workspaceAfterRetention.DatabaseBytes, workspaceBefore.DatabaseBytes)
	}
	if err := itemStore.RebuildStorageQuotaUsage(ctx); err != nil {
		t.Fatalf("rebuild quota after provider audit lifecycle: %v", err)
	}
	rebuilt, err := itemStore.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil || rebuilt.DatabaseBytes != workspaceAfterRetention.DatabaseBytes {
		t.Fatalf("rebuilt provider audit database bytes = %d, %v; want %d", rebuilt.DatabaseBytes, err, workspaceAfterRetention.DatabaseBytes)
	}
}

func TestProviderCallAuditRejectsCrossWorkspaceImage(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceA, imageA := createAnnotationTestResource(t, database, uuid.NewString()+"-audit-a", "https://source.example/canvas/a")
	workspaceB, _ := createAnnotationTestResource(t, database, uuid.NewString()+"-audit-b", "https://source.example/canvas/b")
	err := store.NewProviderCallAuditStore(database).Create(ctx, store.ProviderCallAudit{
		WorkspaceID: workspaceB, ItemImageID: &imageA,
		Provider: "gemini", Model: "model", Operation: "transcribe",
	})
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace audit error = %v", err)
	}
	var count int
	if queryErr := database.QueryRow(`SELECT COUNT(*) FROM provider_call_audits WHERE workspace_id IN (?, ?)`, workspaceA, workspaceB).Scan(&count); queryErr != nil {
		t.Fatalf("count cross-workspace audits: %v", queryErr)
	}
	if count != 0 {
		t.Fatalf("cross-workspace audit count = %d, want 0", count)
	}
}
