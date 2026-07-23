package store

import (
	"context"
	"errors"
	"fmt"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

const (
	// DefaultMaxActiveTranscriptionJobsPerWorkspace bounds durable queue growth
	// when callers do not provide an explicit admission policy.
	DefaultMaxActiveTranscriptionJobsPerWorkspace = 1000
	maxActiveTranscriptionJobsPerWorkspace        = 100000
)

const (
	// MaxTerminalTranscriptionJobsPerWorkspace bounds immutable job/attempt
	// history independently of request rate. Terminal projections discard their
	// transient annotation JSON, and every subsequent admission removes oldest
	// child attempts before their parent job. This avoids an unmetered durable
	// growth path while keeping enough recent history for diagnosis.
	MaxTerminalTranscriptionJobsPerWorkspace int64 = 100
	terminalTranscriptionRetentionBatch      int64 = 100
)

// TranscriptionJobAdmissionPolicy is the immutable per-store durable queue
// policy. Both the regular job repository and canonical reprocess repository
// must receive the same policy.
type TranscriptionJobAdmissionPolicy struct {
	maxActivePerWorkspace int64
}

// NewTranscriptionJobAdmissionPolicy validates a durable queue admission
// limit. Pending and running jobs count toward the limit.
func NewTranscriptionJobAdmissionPolicy(maxActivePerWorkspace int) (TranscriptionJobAdmissionPolicy, error) {
	if maxActivePerWorkspace < 1 || maxActivePerWorkspace > maxActiveTranscriptionJobsPerWorkspace {
		return TranscriptionJobAdmissionPolicy{}, fmt.Errorf("max active transcription jobs per workspace must be between 1 and %d", maxActiveTranscriptionJobsPerWorkspace)
	}
	return TranscriptionJobAdmissionPolicy{maxActivePerWorkspace: int64(maxActivePerWorkspace)}, nil
}

func defaultTranscriptionJobAdmissionPolicy() TranscriptionJobAdmissionPolicy {
	policy, _ := NewTranscriptionJobAdmissionPolicy(DefaultMaxActiveTranscriptionJobsPerWorkspace)
	return policy
}

// TranscriptionJobQuotaExceededError reports a durable workspace queue
// admission rejection. It is intentionally typed so transports can map it to
// ResourceExhausted without string matching.
type TranscriptionJobQuotaExceededError struct {
	WorkspaceID uint64
	Limit       int64
}

func (e *TranscriptionJobQuotaExceededError) Error() string {
	if e == nil {
		return "active transcription job quota exceeded"
	}
	return fmt.Sprintf("workspace active transcription job quota of %d is exhausted", e.Limit)
}

// lockTranscriptionAdmissionWorkspace takes the single per-workspace lock used
// by every job admission path. A count performed after this lock observes every
// earlier admission that committed before the lock was acquired.
func lockTranscriptionAdmissionWorkspace(ctx context.Context, queries *db.Queries, itemImageID uint64) (uint64, error) {
	workspaceID, err := queries.LockTranscriptionJobWorkspaceByItemImageManual(ctx, itemImageID)
	if err != nil {
		return 0, err
	}
	if workspaceID == 0 {
		return 0, errors.New("transcription job workspace is invalid")
	}
	// Leave one slot for an active job that this transaction may supersede. If
	// no supersession occurs the next admission simply keeps the same invariant.
	if err := retainTerminalTranscriptionHistory(ctx, queries, workspaceID, MaxTerminalTranscriptionJobsPerWorkspace-1); err != nil {
		return 0, fmt.Errorf("retain terminal transcription history: %w", err)
	}
	return workspaceID, nil
}

func retainTerminalTranscriptionHistory(ctx context.Context, queries *db.Queries, workspaceID uint64, target int64) error {
	if queries == nil || workspaceID == 0 || target < 0 {
		return fmt.Errorf("workspace and nonnegative retention target are required")
	}
	terminalCount, err := queries.CountTerminalTranscriptionJobsForWorkspaceManual(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("count terminal transcription jobs: %w", err)
	}
	for terminalCount > target {
		batchSize := terminalCount - target
		if batchSize > terminalTranscriptionRetentionBatch {
			batchSize = terminalTranscriptionRetentionBatch
		}
		jobIDs, err := queries.LockOldestTerminalTranscriptionJobsForWorkspaceManual(ctx, db.LockOldestTerminalTranscriptionJobsForWorkspaceManualParams{
			WorkspaceID: workspaceID,
			Limit:       int32(batchSize), // #nosec G115 -- capped by the constant above.
		})
		if err != nil {
			return fmt.Errorf("lock oldest terminal transcription jobs: %w", err)
		}
		if len(jobIDs) == 0 {
			return fmt.Errorf("terminal transcription history changed during retention")
		}
		for _, jobID := range jobIDs {
			jobIDParam := nullableUint64(jobID)
			if err := queries.DetachExternalRequestsFromRetainedJobManual(ctx, db.DetachExternalRequestsFromRetainedJobManualParams{
				WorkspaceID: workspaceID, JobID: jobIDParam,
			}); err != nil {
				return fmt.Errorf("detach retained job from external requests: %w", err)
			}
			if err := queries.DetachUploadBatchFilesFromRetainedJobManual(ctx, db.DetachUploadBatchFilesFromRetainedJobManualParams{
				WorkspaceID: workspaceID, JobID: jobIDParam,
			}); err != nil {
				return fmt.Errorf("detach retained job from upload batch files: %w", err)
			}
			if err := queries.DeleteRetainedTranscriptionJobAttemptsManual(ctx, jobID); err != nil {
				return fmt.Errorf("delete retained transcription attempts: %w", err)
			}
			result, err := queries.DeleteRetainedTerminalTranscriptionJobManual(ctx, db.DeleteRetainedTerminalTranscriptionJobManualParams{
				JobID: jobID, WorkspaceID: workspaceID,
			})
			if err := requireOneAffected(result, err); err != nil {
				return fmt.Errorf("delete retained terminal transcription job: %w", err)
			}
		}
		terminalCount -= int64(len(jobIDs))
	}
	return nil
}

func enforceTranscriptionJobAdmission(
	ctx context.Context,
	queries *db.Queries,
	workspaceID uint64,
	policy TranscriptionJobAdmissionPolicy,
) error {
	active, err := queries.CountActiveTranscriptionJobsByWorkspaceManual(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("count active transcription jobs: %w", err)
	}
	if active >= policy.maxActivePerWorkspace {
		return &TranscriptionJobQuotaExceededError{
			WorkspaceID: workspaceID,
			Limit:       policy.maxActivePerWorkspace,
		}
	}
	return nil
}
