package cloudrunreadiness

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maximumDiagnosticsBytes = 128 << 10

// Diagnostics is a pre-rendered sequence of typed, allowlisted diagnostic
// lines. Its internal representation prevents callers from adding arbitrary
// command output after the trust boundary.
type Diagnostics struct {
	lines []string
}

// Empty reports whether there is any diagnostic content.
func (diagnostics Diagnostics) Empty() bool {
	return len(diagnostics.lines) == 0
}

// Render returns a fresh newline-terminated rendering.
func (diagnostics Diagnostics) Render() []byte {
	if diagnostics.Empty() {
		return nil
	}
	return []byte(strings.Join(diagnostics.lines, "\n") + "\n")
}

type diagnosticsBuilder struct {
	lines []string
}

func newDiagnosticsBuilder(request Request) *diagnosticsBuilder {
	return &diagnosticsBuilder{lines: []string{
		"Scribe Cloud Run readiness diagnostics (typed fields and exact allowlisted markers only)",
		"[readiness] kind=" + string(request.Kind),
		"[readiness] job=" + request.Job,
	}}
}

func (builder *diagnosticsBuilder) readinessStatus(exitCode int, waitStatus string) {
	builder.lines = append(builder.lines,
		"[readiness] execute_status="+strconv.Itoa(exitCode),
		"[status] execution_wait="+safeWaitStatus(waitStatus),
	)
}

func (builder *diagnosticsBuilder) backendStartupRetries(retries int) {
	if retries < 1 {
		return
	}
	builder.lines = append(builder.lines, "[status] backend_startup_retries="+strconv.Itoa(
		boundedNonnegative(retries, productionBackendExecutionAttempts-1),
	))
}

func (builder *diagnosticsBuilder) preflight(category string, observed, maximum int) {
	builder.lines = append(builder.lines, fmt.Sprintf(
		"[status] preflight=failed category=%s observed=%d maximum=%d",
		safeCategory(category),
		boundedNonnegative(observed, 10_000),
		boundedNonnegative(maximum, 10_000),
	))
}

func (builder *diagnosticsBuilder) identity(execution string) {
	if execution == "" {
		builder.lines = append(builder.lines, "[status] execution_identity=unavailable")
		return
	}
	builder.lines = append(builder.lines,
		"[readiness] execution="+execution,
		"[status] execution_identity=ok",
	)
}

func (builder *diagnosticsBuilder) execution(execution Execution, queryStatus string) {
	if queryStatus != "ok" {
		builder.lines = append(builder.lines, "[status] execution_query="+safeQueryStatus(queryStatus))
		return
	}
	builder.lines = append(builder.lines,
		"[execution] name="+execution.Name,
		"[execution] create_time="+safeTimestamp(execution.CreateTime),
		"[execution] start_time="+safeTimestamp(execution.StartTime),
		"[execution] completion_time="+safeTimestamp(execution.CompletionTime),
		"[execution] running_count="+strconv.Itoa(boundedNonnegative(execution.RunningCount, 64)),
		"[execution] succeeded_count="+strconv.Itoa(boundedNonnegative(execution.SucceededCount, 64)),
		"[execution] failed_count="+strconv.Itoa(boundedNonnegative(execution.FailedCount, 64)),
		"[execution] cancelled_count="+strconv.Itoa(boundedNonnegative(execution.CancelledCount, 64)),
		"[execution] retried_count="+strconv.Itoa(boundedNonnegative(execution.RetriedCount, 64)),
		"[execution] state="+safeExecutionState(execution.State),
		"[execution] reason="+safeReason(execution.Reason),
		"[status] execution_query=ok",
	)
}

func (builder *diagnosticsBuilder) tasks(tasks []Task, queryStatus string) {
	if queryStatus != "ok" {
		builder.lines = append(builder.lines, "[status] task_query="+safeQueryStatus(queryStatus))
		return
	}
	if len(tasks) > 4 {
		tasks = tasks[:4]
	}
	for _, task := range tasks {
		builder.lines = append(builder.lines, fmt.Sprintf(
			"[task] index=%s retried=%s exit_code=%s term_signal=%s status_code=%s",
			boundedDiagnosticNumber(task.Index, 63),
			boundedDiagnosticNumber(task.Retried, 63),
			boundedDiagnosticNumber(task.ExitCode, 255),
			boundedDiagnosticNumber(task.TermSignal, 64),
			boundedDiagnosticNumber(task.StatusCode, 16),
		))
	}
	builder.lines = append(builder.lines, "[status] task_query=ok tasks="+strconv.Itoa(len(tasks)))
}

func (builder *diagnosticsBuilder) markers(kind Kind, markers []string, queryStatus string) {
	if queryStatus != "ok" {
		builder.lines = append(builder.lines, "[status] log_query="+safeQueryStatus(queryStatus))
		return
	}
	count := 0
	for _, marker := range markers {
		if count == maximumLogMarkers {
			break
		}
		if ReadinessMarkerAllowed(kind, marker) {
			builder.lines = append(builder.lines, marker)
			count++
		}
	}
	builder.lines = append(builder.lines, "[status] log_query=ok markers="+strconv.Itoa(count))
}

func (builder *diagnosticsBuilder) build() Diagnostics {
	lines := make([]string, len(builder.lines))
	copy(lines, builder.lines)
	return Diagnostics{lines: lines}
}

func safeCategory(category string) string {
	switch category {
	case "active-limit", "candidate-limit", "diagnostics-write", "drain-limit", "identity-ambiguous", "identity-unavailable", "invalid-execution", "list-control-plane", "list-invalid-record", "marker-recovery", "settlement-unconfirmed", "timeout", "control-plane-unavailable", "terminal-failure", "interrupted":
		return category
	default:
		return "unclassified"
	}
}

func safeWaitStatus(status string) string {
	switch status {
	case "cancelled", "control-plane-unavailable", "failed", "identity-unavailable", "invalid-identity", "launch-interrupted", "running", "succeeded", "timeout", "not-started", "interrupted":
		return status
	default:
		return "unknown"
	}
}

func safeQueryStatus(status string) string {
	switch status {
	case "ok", "invalid", "unavailable":
		return status
	default:
		return "unavailable"
	}
}

func safeExecutionState(state ExecutionState) string {
	switch state {
	case ExecutionRunning, ExecutionSucceeded, ExecutionFailed, ExecutionCancelled:
		return string(state)
	default:
		return "unknown"
	}
}

func boundedNonnegative(value, maximum int) int {
	if value < 0 || value > maximum {
		return 0
	}
	return value
}

func boundedDiagnosticNumber(value, maximum int) string {
	if value < 0 || value > maximum {
		return "unknown"
	}
	return strconv.Itoa(value)
}

// writeDiagnostics atomically replaces path from an anchored directory handle.
// It rechecks the target at write time and never follows a target symlink.
func writeDiagnostics(path string, diagnostics Diagnostics) error {
	data := diagnostics.Render()
	if len(data) == 0 || len(data) > maximumDiagnosticsBytes {
		return errors.New("invalid diagnostics")
	}
	if path == "" || strings.ContainsAny(path, "\r\n") || filepath.Ext(path) != ".log" {
		return errors.New("invalid diagnostics path")
	}
	directory, target := filepath.Dir(path), filepath.Base(path)
	root, err := os.OpenRoot(directory)
	if err != nil {
		return errors.New("diagnostics directory unavailable")
	}
	defer root.Close()
	if err := validateRootTarget(root, target); err != nil {
		return err
	}

	randomName := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, randomName); err != nil {
		return errors.New("diagnostics temporary name unavailable")
	}
	temporary := ".scribe-readiness-" + hex.EncodeToString(randomName) + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("diagnostics temporary file unavailable")
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = root.Remove(temporary)
		}
	}()
	written, err := file.Write(data)
	if err != nil || written != len(data) {
		_ = file.Close()
		return errors.New("diagnostics write failed")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("diagnostics sync failed")
	}
	if err := file.Close(); err != nil {
		return errors.New("diagnostics close failed")
	}
	if err := validateRootTarget(root, target); err != nil {
		return err
	}
	if err := root.Rename(temporary, target); err != nil {
		return errors.New("diagnostics replace failed")
	}
	removeTemporary = false
	directoryFile, err := root.Open(".")
	if err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func validateRootTarget(root *os.Root, target string) error {
	if target == "." || target == "" || strings.ContainsAny(target, `/\\\x00`) {
		return errors.New("invalid diagnostics target")
	}
	info, err := root.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("diagnostics target unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("diagnostics target is not a regular file")
	}
	return nil
}
