package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestConcurrentSystemContextEnsureConverges(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	name := "Concurrent system context " + uuid.NewString()
	contexts := store.NewContextStore(database)
	desired := store.Context{
		Name:                  name,
		Description:           "one canonical startup definition",
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "tesseract",
	}

	const callers = 8
	start := make(chan struct{})
	errorsSeen := make(chan error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			<-start
			errorsSeen <- contexts.EnsureSystemContext(ctx, desired)
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent EnsureSystemContext: %v", err)
		}
	}

	var id uint64
	var count int
	if err := database.QueryRowContext(ctx, `SELECT MIN(id), COUNT(*) FROM contexts WHERE workspace_id IS NULL AND name = ?`, name).Scan(&id, &count); err != nil {
		t.Fatalf("load converged system context: %v", err)
	}
	if id == 0 || count != 1 {
		t.Fatalf("converged system contexts = id:%d count:%d, want one", id, count)
	}
	t.Cleanup(func() { _ = contexts.Delete(context.Background(), id) })

	desired.Description = "refreshed canonical startup definition"
	desired.SegmentationModel = "scribe"
	if err := contexts.EnsureSystemContext(ctx, desired); err != nil {
		t.Fatalf("refresh converged system context: %v", err)
	}
	loaded, err := contexts.Get(ctx, id)
	if err != nil || loaded.ID != id || loaded.Description != desired.Description || loaded.SegmentationModel != "scribe" {
		t.Fatalf("refreshed system context = %+v/%v", loaded, err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM contexts WHERE workspace_id IS NULL AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("count refreshed system context: %v", err)
	}
	if count != 1 {
		t.Fatalf("refreshed system context count = %d, want stable row", count)
	}
}

func TestConcurrentEnsureDefaultPromotesExistingNamedContext(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	contexts := store.NewContextStore(database)

	previousDefault, previousDefaultErr := contexts.GetDefault(ctx)
	if previousDefaultErr != nil && !errors.Is(previousDefaultErr, sql.ErrNoRows) {
		t.Fatalf("load previous default context: %v", previousDefaultErr)
	}
	desired, err := contexts.Create(ctx, store.Context{
		Name:                  "Demoted startup default " + uuid.NewString(),
		Description:           "stale startup definition",
		IsDefault:             true,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "stale-model",
	})
	if err != nil {
		t.Fatalf("create startup default: %v", err)
	}
	t.Cleanup(func() {
		if err := contexts.Delete(context.Background(), desired.ID); err != nil {
			t.Errorf("delete startup default context: %v", err)
			return
		}
		if previousDefaultErr == nil {
			previousDefault.IsDefault = true
			if _, err := contexts.Update(context.Background(), previousDefault); err != nil {
				t.Errorf("restore previous default context: %v", err)
			}
		}
	})

	desired.IsDefault = false
	if _, err := contexts.Update(ctx, desired); err != nil {
		t.Fatalf("demote startup default: %v", err)
	}
	desired.Description = "current startup definition"
	desired.TranscriptionModel = "current-model"

	const callers = 8
	start := make(chan struct{})
	errorsSeen := make(chan error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			<-start
			errorsSeen <- contexts.EnsureDefault(ctx, desired)
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent EnsureDefault: %v", err)
		}
	}

	loaded, err := contexts.Get(ctx, desired.ID)
	if err != nil {
		t.Fatalf("load promoted default context: %v", err)
	}
	if !loaded.IsDefault || loaded.Description != desired.Description || loaded.TranscriptionModel != desired.TranscriptionModel {
		t.Fatalf("promoted default context = %+v, want current default definition", loaded)
	}
	var count int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM contexts WHERE workspace_id IS NULL AND name = ?`, desired.Name).Scan(&count); err != nil {
		t.Fatalf("count promoted default contexts: %v", err)
	}
	if count != 1 {
		t.Fatalf("promoted default context count = %d, want 1", count)
	}
}

func TestConcurrentReplaceSystemDefaultRetiresPreviousPreset(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	contexts := store.NewContextStore(database)

	previousDefault, previousDefaultErr := contexts.GetDefault(ctx)
	if previousDefaultErr != nil && !errors.Is(previousDefaultErr, sql.ErrNoRows) {
		t.Fatalf("load previous default context: %v", previousDefaultErr)
	}
	retired, err := contexts.Create(ctx, store.Context{
		Name:                  "Retired system default " + uuid.NewString(),
		IsDefault:             true,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "tesseract",
	})
	if err != nil {
		t.Fatalf("create retired system default: %v", err)
	}
	replacement, err := contexts.Create(ctx, store.Context{
		Name:                  "Replacement system default " + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "tesseract",
	})
	if err != nil {
		t.Fatalf("create replacement system default: %v", err)
	}
	t.Cleanup(func() {
		_ = contexts.Delete(context.Background(), replacement.ID)
		if previousDefaultErr == nil {
			previousDefault.IsDefault = true
			if _, err := contexts.Update(context.Background(), previousDefault); err != nil {
				t.Errorf("restore previous default context: %v", err)
			}
		}
	})

	replacement.IsDefault = true
	const callers = 8
	start := make(chan struct{})
	errorsSeen := make(chan error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			<-start
			errorsSeen <- contexts.ReplaceSystemDefault(ctx, replacement, []string{retired.Name})
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent ReplaceSystemDefault: %v", err)
		}
	}

	if _, err := contexts.Get(ctx, retired.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retired system context still exists: %v", err)
	}
	loaded, err := contexts.GetDefault(ctx)
	if err != nil || loaded.ID != replacement.ID || !loaded.IsDefault {
		t.Fatalf("replacement system default = %+v/%v; want id %d", loaded, err, replacement.ID)
	}
}

func TestConcurrentContextUpdateAndDeleteCannotResurrectRow(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, database)
	contexts := store.NewContextStore(database)

	for iteration := 0; iteration < 10; iteration++ {
		created, err := contexts.Create(ctx, store.Context{
			UserID:                &userID,
			WorkspaceID:           &workspaceID,
			Name:                  "context race " + uuid.NewString(),
			SegmentationModel:     "tesseract",
			TranscriptionProvider: "tesseract",
			TranscriptionModel:    "tesseract",
		})
		if err != nil {
			t.Fatalf("create context race fixture %d: %v", iteration, err)
		}
		updated := created
		updated.Description = "concurrent update"
		start := make(chan struct{})
		updateDone := make(chan error, 1)
		deleteDone := make(chan error, 1)
		go func() {
			<-start
			_, err := contexts.UpdateForWorkspace(ctx, updated, workspaceID, userID)
			updateDone <- err
		}()
		go func() {
			<-start
			deleteDone <- contexts.DeleteForWorkspace(ctx, created.ID, workspaceID)
		}()
		close(start)
		updateErr := <-updateDone
		deleteErr := <-deleteDone
		if deleteErr != nil {
			t.Fatalf("concurrent context delete %d: %v", iteration, deleteErr)
		}
		if updateErr != nil && !errors.Is(updateErr, sql.ErrNoRows) {
			t.Fatalf("concurrent context update %d: %v", iteration, updateErr)
		}
		if _, err := contexts.Get(ctx, created.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("context %d was resurrected after delete: %v", created.ID, err)
		}
	}
}

func TestContextDeletionWaitsForLinkerAndPreservesAuthoritativeSnapshot(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/context-link-" + suffix
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-context-link", canvasURI)
	var userID uint64
	if err := database.QueryRowContext(ctx, `SELECT owner_user_id FROM workspaces WHERE id = ?`, workspaceID).Scan(&userID); err != nil {
		t.Fatalf("load context-link owner: %v", err)
	}
	contexts := store.NewContextStore(database)
	processingContext, err := contexts.Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "authoritative context " + suffix,
		Description:           "stored descriptor",
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "tesseract",
	})
	if err != nil {
		t.Fatalf("create context-link fixture: %v", err)
	}
	annotationStore := store.NewAnnotationStore(database)
	if _, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "baseline"), 0); err != nil {
		t.Fatalf("create context-link canonical page: %v", err)
	}

	pageBlocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin canonical-page blocker: %v", err)
	}
	defer func() { _ = pageBlocker.Rollback() }()
	var revision uint64
	if err := pageBlocker.QueryRowContext(ctx, `
SELECT revision FROM annotation_pages
WHERE workspace_id = ? AND item_image_id = ?
FOR UPDATE`, workspaceID, imageID).Scan(&revision); err != nil {
		t.Fatalf("lock canonical page: %v", err)
	}

	forged := processingContext
	forged.Description = "caller-controlled stale descriptor"
	forged.TranscriptionModel = "forged-model"
	jobDone := make(chan struct {
		id  uint64
		err error
	}, 1)
	go func() {
		id, createErr := store.NewTranscriptionJobStore(database).Create(ctx, imageID, forged)
		jobDone <- struct {
			id  uint64
			err error
		}{id: id, err: createErr}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !contextExclusiveLockIsBlocked(ctx, database, processingContext.ID) {
		if time.Now().After(deadline) {
			t.Fatal("transcription linker did not acquire the shared context lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- contexts.DeleteForWorkspace(ctx, processingContext.ID, workspaceID) }()
	select {
	case err := <-deleteDone:
		t.Fatalf("context deletion bypassed an in-flight linker: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := pageBlocker.Commit(); err != nil {
		t.Fatalf("release canonical-page blocker: %v", err)
	}

	var jobID uint64
	select {
	case result := <-jobDone:
		if result.err != nil {
			t.Fatalf("create transcription job after blocker release: %v", result.err)
		}
		jobID = result.id
	case <-time.After(10 * time.Second):
		t.Fatal("transcription linker did not finish")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete context after linker commit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("context deletion did not resume")
	}

	job, err := store.NewTranscriptionJobStore(database).Get(ctx, jobID)
	if err != nil {
		t.Fatalf("load linked job after context deletion: %v", err)
	}
	if job.ContextID != nil {
		t.Fatalf("deleted context remained linked to job: %d", *job.ContextID)
	}
	var snapshot store.Context
	if err := json.Unmarshal(job.ContextSnapshot, &snapshot); err != nil {
		t.Fatalf("decode immutable context snapshot: %v", err)
	}
	if snapshot.ID != processingContext.ID || snapshot.Description != processingContext.Description || snapshot.TranscriptionModel != processingContext.TranscriptionModel {
		t.Fatalf("job snapshot = %+v, want locked authoritative context %+v", snapshot, processingContext)
	}
}

func contextExclusiveLockIsBlocked(ctx context.Context, database *sql.DB, contextID uint64) bool {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer func() { _ = tx.Rollback() }()
	var lockedID uint64
	err = tx.QueryRowContext(ctx, `SELECT id FROM contexts WHERE id = ? FOR UPDATE NOWAIT`, contextID).Scan(&lockedID)
	return err != nil
}
