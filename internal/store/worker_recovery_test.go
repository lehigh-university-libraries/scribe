package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestRecoveryWakeupsDoNotClaimJobsAndRespectRetryLeases(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvas := "https://source.example/canvas/recovery-" + suffix
	workspace, image := createAnnotationTestResource(t, database, suffix, canvas)
	if _, err := store.NewAnnotationStore(database).SavePage(ctx, canonicalTestPage(t, workspace, image, canvas, "original"), 0); err != nil {
		t.Fatal(err)
	}
	jobs := store.NewTranscriptionJobStore(database)
	id, err := jobs.Create(ctx, image, createAnnotationTestContext(t, database, suffix))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, update string
		eligible     bool
	}{
		{"new pending", "created_at = NOW()", false},
		{"old pending", "created_at = DATE_SUB(NOW(), INTERVAL 10 MINUTE)", true},
		{"retry delayed", "retry_after = DATE_ADD(NOW(), INTERVAL 10 MINUTE)", false},
		{"retry due", "retry_after = DATE_SUB(NOW(), INTERVAL 1 MINUTE)", true},
		{"live lease", "lease_until = DATE_ADD(NOW(), INTERVAL 10 MINUTE)", false},
		{"expired lease", "lease_until = DATE_SUB(NOW(), INTERVAL 1 MINUTE)", true},
		{"exhausted pending", "status = 'pending', attempt_count = max_attempts, locked_by = NULL, lease_until = NULL", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "live lease" {
				claimed, err := jobs.ClaimPendingByID(ctx, id)
				if err != nil || claimed == nil {
					t.Fatalf("claim live attempt: %v/%v", claimed, err)
				}
			}
			if _, err := database.ExecContext(ctx, "UPDATE transcription_jobs SET "+test.update+" WHERE id = ?", id); err != nil {
				t.Fatal(err)
			} // #nosec G202 -- SQL fragments are fixed test table literals; the resource ID remains parameterized.
			before, err := jobs.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			ids, err := jobs.RecoverableJobIDs(ctx, time.Now().UTC().Add(-time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, candidate := range ids {
				if candidate == id {
					found = true
				}
			}
			if found != test.eligible {
				t.Fatalf("eligible = %v; want %v", found, test.eligible)
			}
			after, err := jobs.Get(ctx, id)
			if err != nil || after.AttemptCount != before.AttemptCount || after.Status != before.Status {
				t.Fatalf("wake-up query mutated attempt: before=%+v after=%+v error=%v", before, after, err)
			}
		})
	}
}
