package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	dbgen "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestWorkspaceSelectionRuleAdmissionIsSerializedAndResolutionIsBounded(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, database)
	contexts := store.NewContextStore(database)
	processingContext, err := contexts.Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "rule-cap-" + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "eng",
	})
	if err != nil {
		t.Fatalf("create processing context: %v", err)
	}

	var existing int
	if err := database.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE c.workspace_id IS NULL OR c.workspace_id = ?
`, workspaceID).Scan(&existing); err != nil {
		t.Fatalf("count initial visible rules: %v", err)
	}
	if existing >= store.MaxSelectionRulesPerWorkspace {
		t.Fatalf("test database already has %d visible system rules; limit is %d", existing, store.MaxSelectionRulesPerWorkspace)
	}
	conditions := `[{
  "field": "source_type",
  "operator": "eq",
  "value": "manifest"
}]`
	for index := existing; index < store.MaxSelectionRulesPerWorkspace-1; index++ {
		if _, err := database.ExecContext(ctx, `
INSERT INTO context_selection_rules (context_id, priority, conditions)
VALUES (?, ?, ?)
`, processingContext.ID, index, conditions); err != nil {
			t.Fatalf("seed rule %d: %v", index, err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(priority int32) {
			defer wait.Done()
			<-start
			_, err := contexts.CreateRuleForWorkspace(ctx, workspaceID, store.ContextSelectionRule{
				ContextID:  processingContext.ID,
				Priority:   priority,
				Conditions: []store.RuleCondition{{Field: "language", Operator: "eq", Value: "en"}},
			})
			results <- err
		}(int32(index + 1000))
	}
	close(start)
	wait.Wait()
	close(results)

	succeeded, exhausted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, store.ErrSelectionRuleLimit):
			exhausted++
		default:
			t.Fatalf("concurrent rule admission: %v", err)
		}
	}
	if succeeded != 1 || exhausted != 1 {
		t.Fatalf("admission outcomes = success:%d exhausted:%d, want 1/1", succeeded, exhausted)
	}

	var visible int
	if err := database.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE c.workspace_id IS NULL OR c.workspace_id = ?
`, workspaceID).Scan(&visible); err != nil {
		t.Fatalf("count admitted rules: %v", err)
	}
	if visible != store.MaxSelectionRulesPerWorkspace {
		t.Fatalf("visible rules = %d, want %d", visible, store.MaxSelectionRulesPerWorkspace)
	}

	// Simulate out-of-band corruption or a future bad writer bypassing admission.
	// Resolution must detect max+1 rather than silently scanning or truncating.
	if _, err := database.ExecContext(ctx, `
INSERT INTO context_selection_rules (context_id, priority, conditions)
VALUES (?, ?, ?)
`, processingContext.ID, 2000, conditions); err != nil {
		t.Fatalf("insert overflow rule: %v", err)
	}
	if _, _, err := contexts.ResolveForWorkspace(ctx, workspaceID, map[string]any{"source_type": "manifest"}); !errors.Is(err, store.ErrContextResolutionLimit) {
		t.Fatalf("overflow resolution error = %v, want ErrContextResolutionLimit", err)
	}

	if _, err := contexts.CreateRuleForWorkspace(ctx, workspaceID, store.ContextSelectionRule{
		ContextID:  processingContext.ID,
		Conditions: []store.RuleCondition{{Field: "x", Operator: "eq", Value: fmt.Sprint(workspaceID)}},
	}); !errors.Is(err, store.ErrSelectionRuleLimit) {
		t.Fatalf("post-overflow admission error = %v, want ErrSelectionRuleLimit", err)
	}
}

func TestDeletingUsedContextPreservesImmutableSnapshots(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, database)
	contexts := store.NewContextStore(database)
	processingContext, err := contexts.Create(ctx, store.Context{
		UserID: &userID, WorkspaceID: &workspaceID, Name: "deletable-" + uuid.NewString(),
		SegmentationModel: "tesseract", TranscriptionProvider: "tesseract", TranscriptionModel: "eng",
	})
	if err != nil {
		t.Fatalf("create processing context: %v", err)
	}

	itemStore := store.NewItemStore(database)
	itemID := "context-delete-" + uuid.NewString()
	if _, err := itemStore.Create(ctx, dbgen.CreateItemParams{
		ID: itemID, UserID: userID, WorkspaceID: workspaceID,
		Name: "context deletion fixture", SourceType: "manifest",
	}); err != nil {
		t.Fatalf("create item: %v", err)
	}
	canvasURI := "https://source.example/canvas/" + uuid.NewString()
	image, err := itemStore.AddImage(ctx, dbgen.CreateItemImageParams{
		ItemID: itemID, ImageURL: "https://images.example/context-delete.jpg",
		CanvasURI: canvasURI, Width: 100, Height: 100,
	})
	if err != nil {
		t.Fatalf("create item image: %v", err)
	}
	if _, err := store.NewAnnotationStore(database).SavePage(
		ctx, canonicalTestPage(t, workspaceID, image.ID, canvasURI, "baseline"), 0,
	); err != nil {
		t.Fatalf("create canonical page: %v", err)
	}

	jobStore := store.NewTranscriptionJobStore(database)
	forgedContext := processingContext
	forgedContext.Name = "caller-forged"
	forgedContext.SegmentationModel = "forged-segmentor"
	forgedContext.TranscriptionProvider = "forged-provider"
	forgedContext.TranscriptionModel = "forged-model"
	jobID, err := jobStore.Create(ctx, image.ID, forgedContext)
	if err != nil {
		t.Fatalf("create transcription job: %v", err)
	}
	contextID := processingContext.ID
	if err := store.NewOCRRunStore(database).Create(ctx, store.OCRRun{
		SessionID: "context-delete-" + uuid.NewString(), ItemImageID: &image.ID, ContextID: &contextID,
		ImageURL: image.ImageURL, Provider: "tesseract", Model: "eng",
		OriginalHOCR: "<html></html>", OriginalText: "baseline",
	}); err != nil {
		t.Fatalf("create OCR run: %v", err)
	}
	auditSession := "context-delete-audit-" + uuid.NewString()
	if err := store.NewProviderCallAuditStore(database).Create(ctx, store.ProviderCallAudit{
		WorkspaceID: workspaceID, SessionID: auditSession, ItemImageID: &image.ID, ContextID: &contextID,
		Provider: "tesseract", Model: "eng", Operation: "transcribe",
	}); err != nil {
		t.Fatalf("create provider audit: %v", err)
	}
	batchID := "context-delete-batch-" + uuid.NewString()
	if _, err := itemStore.StartUploadBatch(ctx, store.StartUploadBatchParams{
		WorkspaceID: workspaceID, UserID: userID, BatchID: batchID,
		ItemID: "context-delete-batch-item-" + uuid.NewString(), Name: "context snapshot batch",
		Context: forgedContext, RequestHash: fmt.Sprintf("%064x", contextID),
		Files: []store.UploadBatchFileInput{{Filename: "page.png", Size: 1, ContentSHA256: fmt.Sprintf("%064x", contextID+1)}},
	}); err != nil {
		t.Fatalf("create upload batch: %v", err)
	}

	if err := contexts.DeleteForWorkspace(ctx, contextID, workspaceID); err != nil {
		t.Fatalf("delete used context: %v", err)
	}
	if _, err := contexts.GetForWorkspace(ctx, contextID, workspaceID); err == nil {
		t.Fatal("deleted context remains readable")
	}

	job, err := jobStore.Get(ctx, jobID)
	if err != nil || job.ContextID != nil {
		t.Fatalf("job after context deletion = %+v/%v", job, err)
	}
	var jobContext store.Context
	if err := json.Unmarshal(job.ContextSnapshot, &jobContext); err != nil || jobContext.ID != contextID {
		t.Fatalf("job context snapshot = %+v/%v", jobContext, err)
	}
	if jobContext.Name != processingContext.Name || jobContext.TranscriptionModel != processingContext.TranscriptionModel {
		t.Fatalf("job context snapshot used caller fields = %+v, want authoritative %+v", jobContext, processingContext)
	}
	batch, err := itemStore.GetUploadBatch(ctx, workspaceID, batchID)
	if err != nil || batch.ContextID != 0 {
		t.Fatalf("batch after context deletion = %+v/%v", batch, err)
	}
	batchContext, err := batch.Context()
	if err != nil || batchContext.ID != contextID {
		t.Fatalf("batch context snapshot = %+v/%v", batchContext, err)
	}
	if batchContext.Name != processingContext.Name || batchContext.TranscriptionModel != processingContext.TranscriptionModel {
		t.Fatalf("batch context snapshot used caller fields = %+v, want authoritative %+v", batchContext, processingContext)
	}

	for table, predicate := range map[string]string{
		"ocr_runs":             "item_image_id = ?",
		"transcription_jobs":   "id = ?",
		"upload_batches":       "workspace_id = ? AND id = ?",
		"provider_call_audits": "workspace_id = ? AND session_id = ?",
	} {
		args := []any{image.ID}
		switch table {
		case "transcription_jobs":
			args = []any{jobID}
		case "upload_batches":
			args = []any{workspaceID, batchID}
		case "provider_call_audits":
			args = []any{workspaceID, auditSession}
		}
		query := "SELECT COUNT(*) FROM " + table + " WHERE " + predicate + " AND (context_id IS NOT NULL OR context_scope_id IS NOT NULL)"
		var linked int
		if err := database.QueryRowContext(ctx, query, args...).Scan(&linked); err != nil || linked != 0 {
			t.Fatalf("%s retained deleted context link = %d/%v", table, linked, err)
		}
	}
}
