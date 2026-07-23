package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitForTranscriptionWorkersHonorsShutdownDeadline(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	release := make(chan struct{})
	handler.startTranscriptionWorker(func() { <-release })

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := handler.WaitForTranscriptionWorkers(timeoutCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForTranscriptionWorkers() error = %v; want deadline exceeded", err)
	}
	close(release)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := handler.WaitForTranscriptionWorkers(waitCtx); err != nil {
		t.Fatalf("WaitForTranscriptionWorkers() after release error = %v", err)
	}
}

func TestWaitForBackgroundWorkersIncludesMaintenanceAndTranscription(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	releaseTranscription := make(chan struct{})
	releaseMaintenance := make(chan struct{})
	handler.startTranscriptionWorker(func() { <-releaseTranscription })
	handler.startBackgroundWorker(func() { <-releaseMaintenance })

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := handler.WaitForBackgroundWorkers(timeoutCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForBackgroundWorkers() error = %v; want deadline exceeded", err)
	}
	close(releaseTranscription)

	transcriptionOnlyCtx, transcriptionOnlyCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer transcriptionOnlyCancel()
	if err := handler.WaitForBackgroundWorkers(transcriptionOnlyCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForBackgroundWorkers() with maintenance active error = %v; want deadline exceeded", err)
	}
	close(releaseMaintenance)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := handler.WaitForBackgroundWorkers(waitCtx); err != nil {
		t.Fatalf("WaitForBackgroundWorkers() after release error = %v", err)
	}
}
