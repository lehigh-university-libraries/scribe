package cloudrunreadiness

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

const (
	ExitSuccess           = 0
	ExitReadinessFailed   = 1
	ExitInvalidInvocation = 2
	ExitTimedOut          = 124
	ExitControlPlane      = 125
	ExitSettlementFailed  = 126
	ExitInterrupted       = 130
	ExitTerminated        = 143

	uniqueMarkerAttempts               = 4
	nonBackendExecutionAttempts        = 1
	previewBackendExecutionAttempts    = 2
	productionBackendExecutionAttempts = 6
	productionBackendReadinessJob      = "scribe-prod-backend-readiness"

	backendStartupGateRetryMarker = "frontend backend startup gate failed [Error; startup-deadline; transport-AbortError]"
	backendFrontendRetryMarker    = "frontend readiness failed: transport-AbortError"
	backendNetworkRetryMarker     = "frontend backend network probe [dns-match; tcp-timeout; http-timeout]"
)

var errExecutionDeadline = errors.New("cloud run readiness execution deadline exceeded")

// Limits bounds every lifecycle loop and response set.
type Limits struct {
	HistoryExecutions        int
	ActiveExecutions         int
	PreflightPasses          int
	MarkerRecoveryAttempts   int
	ConsecutiveQueryFailures int
	ExecutionPolls           int
	SettlementPolls          int
	PollInterval             time.Duration
	ExecutionTimeout         time.Duration
	SettlementTimeout        time.Duration
	CleanupTimeout           time.Duration
}

// DefaultLimits matches Cloud Run's retained execution bound and the existing
// readiness operational contract.
func DefaultLimits() Limits {
	return Limits{
		HistoryExecutions:        2000,
		ActiveExecutions:         16,
		PreflightPasses:          3,
		MarkerRecoveryAttempts:   12,
		ConsecutiveQueryFailures: 3,
		ExecutionPolls:           540,
		SettlementPolls:          540,
		PollInterval:             5 * time.Second,
		ExecutionTimeout:         45 * time.Minute,
		SettlementTimeout:        45 * time.Minute,
		CleanupTimeout:           45 * time.Minute,
	}
}

// Result is the complete, non-error lifecycle outcome.
type Result struct {
	ExitCode    int
	Category    string
	Execution   string
	Diagnostics Diagnostics
}

// RunnerOption customizes bounded runner behavior, primarily for focused tests.
type RunnerOption func(*Runner)

// WithLimits replaces the default lifecycle bounds. Invalid or zero fields are
// restored to their defaults by NewRunner.
func WithLimits(limits Limits) RunnerOption {
	return func(runner *Runner) {
		runner.limits = limits
	}
}

// WithWait replaces timer waiting without changing lifecycle attempt bounds.
func WithWait(wait func(context.Context, time.Duration) error) RunnerOption {
	return func(runner *Runner) {
		if wait != nil {
			runner.wait = wait
		}
	}
}

// WithMarkerSource supplies marker entropy and the PID component.
func WithMarkerSource(source io.Reader, pid int) RunnerOption {
	return func(runner *Runner) {
		if source != nil {
			runner.random = source
		}
		if pid > 0 {
			runner.pid = pid
		}
	}
}

// Runner owns preflight fencing, execution identity recovery, terminal waits,
// settlement, and redacted failure diagnostics.
type Runner struct {
	client Client
	limits Limits
	wait   func(context.Context, time.Duration) error
	random io.Reader
	pid    int
}

// NewRunner constructs a lifecycle runner around a typed Client.
func NewRunner(client Client, options ...RunnerOption) *Runner {
	runner := &Runner{
		client: client,
		limits: DefaultLimits(),
		wait:   waitForTimer,
		random: rand.Reader,
		pid:    os.Getpid(),
	}
	for _, option := range options {
		if option != nil {
			option(runner)
		}
	}
	runner.limits = normalizeLimits(runner.limits)
	return runner
}

// SignalCause creates the typed context cancellation cause used to preserve
// SIGINT (130) and SIGTERM (143) through bounded cleanup.
func SignalCause(exitCode int) error {
	if exitCode != ExitInterrupted && exitCode != ExitTerminated {
		exitCode = ExitInterrupted
	}
	return signalCause{exitCode: exitCode}
}

type signalCause struct {
	exitCode int
}

func (cause signalCause) Error() string { return "readiness interrupted" }

type runFailure struct {
	exitCode int
	category string
	status   string
	observed int
	maximum  int
	terminal bool
}

// Run executes the complete lifecycle. It only writes validated names, fixed
// messages, and rendered typed diagnostics to the provided streams.
func (runner *Runner) Run(ctx context.Context, request Request, stdout, stderr io.Writer) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if err := ValidateRequest(request); err != nil {
		_, _ = fmt.Fprintln(stderr, "Cloud Run readiness helper failed: invalid request")
		return Result{ExitCode: ExitInvalidInvocation, Category: "invalid-request"}
	}
	if runner == nil || runner.client == nil {
		_, _ = fmt.Fprintln(stderr, "Cloud Run readiness helper failed: control plane unavailable")
		return runnerFailureResult(request, ExitControlPlane, "control-plane-unavailable", "not-started", "", stderr)
	}

	baseline, preflightFailure := runner.preflight(ctx, request, stderr)
	if preflightFailure != nil {
		if contextStopped(ctx) && preflightFailure.category != "settlement-unconfirmed" {
			preflightFailure.exitCode = contextExitCode(ctx)
			preflightFailure.category = "interrupted"
			preflightFailure.status = "interrupted"
		}
		_, _ = fmt.Fprintf(stderr,
			"Cloud Run %s readiness preflight could not confirm a clear execution set for %s [category=%s observed=%d maximum=%d].\n",
			request.Kind, request.Job, safeCategory(preflightFailure.category), preflightFailure.observed, preflightFailure.maximum)
		return runner.preflightFailureResult(request, *preflightFailure, stderr)
	}
	if contextStopped(ctx) {
		return runnerFailureResult(request, contextExitCode(ctx), "interrupted", "interrupted", "", stderr)
	}
	if request.PreflightOnly {
		_, _ = fmt.Fprintf(stdout, "Cloud Run %s readiness preflight is clear for %s.\n", request.Kind, request.Job)
		return Result{ExitCode: ExitSuccess}
	}

	executionContext, cancelExecution := context.WithTimeoutCause(ctx, runner.limits.ExecutionTimeout, errExecutionDeadline)
	defer cancelExecution()

	executionAttempts := readinessExecutionAttempts(request)
	issuedMarkers := make(map[string]struct{}, executionAttempts)
	var retryCandidateFailure *runFailure
	var retryCandidateExecution string
	retriesStarted := 0
	for attempt := 0; attempt < executionAttempts; attempt++ {
		marker, err := runner.executionMarkerExcluding(issuedMarkers)
		if err != nil {
			if retryCandidateFailure != nil {
				return runner.finishExecutionFailure(request, retryCandidateExecution, retryCandidateFailure, retriesStarted, stderr)
			}
			return runnerFailureResult(request, ExitControlPlane, "identity-unavailable", "identity-unavailable", "", stderr)
		}
		issuedMarkers[marker] = struct{}{}
		if attempt > 0 {
			retriesStarted++
			_, _ = fmt.Fprintln(stdout, "Retrying Cloud Run backend readiness after confirmed guest-startup lag.")
		}
		environment := map[string]string{"SCRIBE_READINESS_EXECUTION_ID": marker}
		if productionBrowserJob.MatchString(request.Job) {
			environment[browserSecretVersionEnvironment] = request.BrowserExpectedSecretVersion
			environment[browserStateDigestEnvironment] = request.BrowserExpectedStorageStateSHA256
		}
		_, _ = fmt.Fprintf(stdout, "Running Cloud Run %s readiness job %s.\n", request.Kind, request.Job)
		execution, waitStatus, owned := runner.launchOwnedExecution(executionContext, request, marker, environment, baseline)
		if !owned {
			return runner.finishFailureWithRetry(
				request,
				Result{ExitCode: ExitSettlementFailed, Category: "settlement-unconfirmed"},
				waitStatusForIdentity(waitStatus),
				"",
				retriesStarted,
				stderr,
			)
		}
		if contextStopped(executionContext) {
			return runner.finishExecutionFailure(request, execution, stoppedExecutionFailure(executionContext), retriesStarted, stderr)
		}

		waitFailure := runner.waitForExecution(
			executionContext,
			request,
			execution,
			runner.limits.ExecutionPolls,
			runner.limits.ExecutionTimeout,
		)
		if waitFailure == nil {
			if contextStopped(executionContext) {
				return runner.finishExecutionFailure(request, execution, stoppedExecutionFailure(executionContext), retriesStarted, stderr)
			}
			_, _ = fmt.Fprintf(stdout, "Cloud Run %s readiness passed for %s.\n", request.Kind, request.Job)
			return Result{ExitCode: ExitSuccess, Execution: execution}
		}
		if contextStopped(executionContext) {
			return runner.finishExecutionFailure(request, execution, stoppedExecutionFailure(executionContext), retriesStarted, stderr)
		}
		retryQualified := attempt+1 < executionAttempts && runner.backendStartupRetryQualified(executionContext, request, execution, waitFailure)
		if contextStopped(executionContext) {
			return runner.finishExecutionFailure(request, execution, stoppedExecutionFailure(executionContext), retriesStarted, stderr)
		}
		if retryQualified {
			retryCandidateExecution = execution
			retryCandidateFailure = waitFailure
			continue
		}
		return runner.finishExecutionFailure(request, execution, waitFailure, retriesStarted, stderr)
	}

	return runner.finishExecutionFailure(request, retryCandidateExecution, retryCandidateFailure, retriesStarted, stderr)
}

func readinessExecutionAttempts(request Request) int {
	if request.Kind != KindBackend {
		return nonBackendExecutionAttempts
	}
	if request.Job == productionBackendReadinessJob {
		return productionBackendExecutionAttempts
	}
	return previewBackendExecutionAttempts
}

func (runner *Runner) launchOwnedExecution(
	ctx context.Context,
	request Request,
	marker string,
	environment map[string]string,
	baseline map[string]struct{},
) (string, string, bool) {
	launch, _ := runner.client.Execute(ctx, request.Job, environment)
	execution := ""
	waitStatus := "not-started"
	if launch.Candidate != "" {
		if leaf, identityErr := executionLeaf(request.Project, request.Region, request.Job, launch.Candidate); identityErr == nil {
			if verified, ok := runner.verifyOwnedExecution(ctx, request, leaf, marker); ok {
				execution = verified.Name
				waitStatus = "running"
			} else {
				waitStatus = "invalid-identity"
			}
		} else {
			waitStatus = "invalid-identity"
		}
	}
	if execution != "" {
		return execution, waitStatus, true
	}
	recoveryContext, cancelRecovery := runner.recoveryContext(ctx)
	recovered, recoveryFailure := runner.recoverFromMarker(recoveryContext, request, marker, baseline)
	cancelRecovery()
	if recoveryFailure != nil {
		return "", "identity-unavailable", false
	}
	return recovered.Name, "launch-interrupted", true
}

func (runner *Runner) backendStartupRetryQualified(
	ctx context.Context,
	request Request,
	execution string,
	failure *runFailure,
) bool {
	if request.Kind != KindBackend || execution == "" || failure == nil ||
		!failure.terminal || failure.category != "terminal-failure" || failure.status != "failed" ||
		contextStopped(ctx) {
		return false
	}
	markers, err := runner.client.ReadinessMarkers(ctx, request.Job, execution, request.Kind)
	if err != nil || contextStopped(ctx) {
		return false
	}
	return exactBackendStartupRetryMarkers(markers)
}

func exactBackendStartupRetryMarkers(markers []string) bool {
	if len(markers) != 3 {
		return false
	}
	wanted := map[string]bool{
		backendStartupGateRetryMarker: false,
		backendFrontendRetryMarker:    false,
		backendNetworkRetryMarker:     false,
	}
	for _, marker := range markers {
		seen, known := wanted[marker]
		if !known || seen {
			return false
		}
		wanted[marker] = true
	}
	for _, seen := range wanted {
		if !seen {
			return false
		}
	}
	return true
}

func (runner *Runner) finishExecutionFailure(
	request Request,
	execution string,
	failure *runFailure,
	startupRetries int,
	stderr io.Writer,
) Result {
	if failure == nil {
		return runner.finishFailureWithRetry(
			request,
			Result{ExitCode: ExitControlPlane, Category: "control-plane-unavailable", Execution: execution},
			"control-plane-unavailable",
			execution,
			startupRetries,
			stderr,
		)
	}
	if failure.category == "interrupted" {
		return runner.finishInterruptedWithRetry(request, execution, failure.exitCode, startupRetries, stderr)
	}
	result := Result{ExitCode: failure.exitCode, Category: failure.category, Execution: execution}
	if !failure.terminal && execution != "" {
		if !runner.settleFresh(request, execution, stderr) {
			result.ExitCode = ExitSettlementFailed
			result.Category = "settlement-unconfirmed"
		}
	}
	return runner.finishFailureWithRetry(request, result, failure.status, execution, startupRetries, stderr)
}

func stoppedExecutionFailure(ctx context.Context) *runFailure {
	if errors.Is(context.Cause(ctx), errExecutionDeadline) {
		return &runFailure{exitCode: ExitTimedOut, category: "timeout", status: "timeout"}
	}
	return &runFailure{exitCode: contextExitCode(ctx), category: "interrupted", status: "interrupted"}
}

func (runner *Runner) preflight(ctx context.Context, request Request, stderr io.Writer) (map[string]struct{}, *runFailure) {
	for pass := 0; pass < runner.limits.PreflightPasses; pass++ {
		executions, names, failure := runner.activeExecutions(ctx, request)
		if failure != nil {
			return nil, failure
		}
		if len(executions) == 0 {
			return names, nil
		}
		for _, execution := range executions {
			if contextStopped(ctx) {
				return nil, &runFailure{exitCode: contextExitCode(ctx), category: "interrupted", status: "interrupted"}
			}
			failure := runner.settle(ctx, request, execution.Name, stderr)
			if failure != nil {
				if failure.category == "interrupted" {
					if !runner.settleFresh(request, execution.Name, stderr) {
						return nil, &runFailure{exitCode: ExitSettlementFailed, category: "settlement-unconfirmed", status: "interrupted"}
					}
				}
				return nil, failure
			}
		}
	}
	return nil, &runFailure{
		exitCode: ExitSettlementFailed,
		category: "drain-limit",
		status:   "not-started",
		observed: runner.limits.PreflightPasses,
		maximum:  runner.limits.PreflightPasses,
	}
}

func (runner *Runner) activeExecutions(ctx context.Context, request Request) ([]Execution, map[string]struct{}, *runFailure) {
	records, err := runner.client.ListExecutions(ctx, request.Job, runner.limits.HistoryExecutions)
	if err != nil {
		if contextStopped(ctx) {
			return nil, nil, &runFailure{
				exitCode: contextExitCode(ctx),
				category: "interrupted",
				status:   "interrupted",
			}
		}
		return nil, nil, &runFailure{exitCode: ExitSettlementFailed, category: "list-control-plane", status: "not-started"}
	}
	if len(records) > runner.limits.HistoryExecutions {
		return nil, nil, &runFailure{exitCode: ExitSettlementFailed, category: "candidate-limit", observed: len(records), maximum: runner.limits.HistoryExecutions}
	}
	active := make([]Execution, 0, min(len(records), runner.limits.ActiveExecutions+1))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		validated, validationErr := validateExecution(record, request.Project, request.Region, request.Job)
		if validationErr != nil {
			return nil, nil, &runFailure{exitCode: ExitSettlementFailed, category: "list-invalid-record"}
		}
		if _, duplicate := seen[validated.Name]; duplicate {
			return nil, nil, &runFailure{exitCode: ExitSettlementFailed, category: "list-invalid-record"}
		}
		seen[validated.Name] = struct{}{}
		if validated.State == ExecutionRunning {
			active = append(active, validated)
		}
	}
	if len(active) > runner.limits.ActiveExecutions {
		return nil, nil, &runFailure{exitCode: ExitSettlementFailed, category: "active-limit", observed: len(active), maximum: runner.limits.ActiveExecutions}
	}
	return active, seen, nil
}

func (runner *Runner) recoverFromMarker(ctx context.Context, request Request, marker string, baseline map[string]struct{}) (Execution, *runFailure) {
	for attempt := 0; attempt < runner.limits.MarkerRecoveryAttempts; attempt++ {
		records, err := runner.client.ListExecutions(ctx, request.Job, runner.limits.HistoryExecutions)
		if err == nil && len(records) <= runner.limits.HistoryExecutions {
			matches := make(map[string]Execution)
			active := make([]Execution, 0)
			newNames := make([]string, 0)
			seen := make(map[string]struct{}, len(records))
			validRecords := true
			for _, record := range records {
				validated, validationErr := validateExecution(record, request.Project, request.Region, request.Job)
				if validationErr != nil {
					validRecords = false
					break
				}
				if _, duplicate := seen[validated.Name]; duplicate {
					validRecords = false
					break
				}
				seen[validated.Name] = struct{}{}
				if validated.State == ExecutionRunning {
					active = append(active, validated)
				}
				if _, existed := baseline[validated.Name]; !existed {
					newNames = append(newNames, validated.Name)
				}
			}
			if !validRecords {
				return Execution{}, &runFailure{exitCode: ExitControlPlane, category: "list-invalid-record"}
			}
			if len(active) > runner.limits.ActiveExecutions {
				return Execution{}, &runFailure{exitCode: ExitControlPlane, category: "active-limit", observed: len(active), maximum: runner.limits.ActiveExecutions}
			}
			if len(newNames) > runner.limits.ActiveExecutions {
				return Execution{}, &runFailure{exitCode: ExitControlPlane, category: "candidate-limit", observed: len(newNames), maximum: runner.limits.ActiveExecutions}
			}
			allCandidatesDescribed := true
			for _, candidate := range newNames {
				described, describeErr := runner.client.DescribeExecution(ctx, request.Job, candidate)
				if describeErr != nil {
					allCandidatesDescribed = false
					break
				}
				validated, validationErr := validateExecution(described, request.Project, request.Region, request.Job)
				if validationErr != nil || validated.Name != candidate {
					allCandidatesDescribed = false
					break
				}
				if validated.ReadinessMarker == marker {
					matches[validated.Name] = validated
				}
			}
			if !allCandidatesDescribed {
				if attempt+1 < runner.limits.MarkerRecoveryAttempts {
					if waitErr := runner.wait(ctx, runner.limits.PollInterval); waitErr != nil {
						return Execution{}, &runFailure{exitCode: contextExitCode(ctx), category: "interrupted", status: "interrupted"}
					}
				}
				continue
			}
			switch len(matches) {
			case 1:
				for _, match := range matches {
					return match, nil
				}
			default:
				if len(matches) > 1 {
					return Execution{}, &runFailure{exitCode: ExitControlPlane, category: "identity-ambiguous", observed: len(matches), maximum: 1}
				}
			}
		} else if err == nil {
			return Execution{}, &runFailure{exitCode: ExitControlPlane, category: "candidate-limit", observed: len(records), maximum: runner.limits.HistoryExecutions}
		}
		if attempt+1 < runner.limits.MarkerRecoveryAttempts {
			if waitErr := runner.wait(ctx, runner.limits.PollInterval); waitErr != nil {
				return Execution{}, &runFailure{exitCode: contextExitCode(ctx), category: "interrupted", status: "interrupted"}
			}
		}
	}
	return Execution{}, &runFailure{exitCode: ExitControlPlane, category: "marker-recovery"}
}

func (runner *Runner) verifyOwnedExecution(ctx context.Context, request Request, execution, marker string) (Execution, bool) {
	if contextStopped(ctx) {
		return Execution{}, false
	}
	described, err := runner.client.DescribeExecution(ctx, request.Job, execution)
	if err != nil {
		return Execution{}, false
	}
	validated, err := validateExecution(described, request.Project, request.Region, request.Job)
	if err != nil || validated.Name != execution || validated.ReadinessMarker != marker {
		return Execution{}, false
	}
	return validated, true
}

func (runner *Runner) waitForExecution(ctx context.Context, request Request, execution string, polls int, timeout time.Duration) *runFailure {
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	queryFailures := 0
	for attempt := 0; attempt < polls; attempt++ {
		if contextStopped(waitContext) {
			return waitContextFailure(ctx)
		}
		record, err := runner.client.DescribeExecution(waitContext, request.Job, execution)
		if err != nil {
			queryFailures++
		} else {
			validated, validationErr := validateExecution(record, request.Project, request.Region, request.Job)
			if validationErr != nil || validated.Name != execution {
				queryFailures++
			} else {
				queryFailures = 0
				if contextStopped(waitContext) {
					return waitContextFailure(ctx)
				}
				switch validated.State {
				case ExecutionSucceeded:
					return nil
				case ExecutionFailed:
					return &runFailure{exitCode: ExitReadinessFailed, category: "terminal-failure", status: "failed", terminal: true}
				case ExecutionCancelled:
					return &runFailure{exitCode: ExitReadinessFailed, category: "terminal-failure", status: "cancelled", terminal: true}
				}
			}
		}
		if contextStopped(waitContext) {
			return waitContextFailure(ctx)
		}
		if queryFailures >= runner.limits.ConsecutiveQueryFailures {
			return &runFailure{exitCode: ExitControlPlane, category: "control-plane-unavailable", status: "control-plane-unavailable"}
		}
		if attempt+1 < polls {
			if waitErr := runner.wait(waitContext, runner.limits.PollInterval); waitErr != nil {
				return waitContextFailure(ctx)
			}
		}
	}
	return &runFailure{exitCode: ExitTimedOut, category: "timeout", status: "timeout"}
}

func (runner *Runner) settle(ctx context.Context, request Request, execution string, stderr io.Writer) *runFailure {
	if request.Kind == KindBrowser {
		_, _ = fmt.Fprintf(stderr, "Waiting for Cloud Run browser readiness execution %s to finish cleanup.\n", execution)
	} else {
		_, _ = fmt.Fprintf(stderr, "Cancelling Cloud Run %s readiness execution %s.\n", request.Kind, execution)
		_ = runner.client.CancelExecution(ctx, request.Job, execution)
	}
	failure := runner.waitForExecution(ctx, request, execution, runner.limits.SettlementPolls, runner.limits.SettlementTimeout)
	if failure == nil || failure.terminal {
		return nil
	}
	if failure.category == "interrupted" {
		return failure
	}
	return &runFailure{exitCode: ExitSettlementFailed, category: "settlement-unconfirmed", status: failure.status}
}

func (runner *Runner) settleFresh(request Request, execution string, stderr io.Writer) bool {
	if execution == "" {
		return true
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), runner.limits.CleanupTimeout)
	defer cancel()
	return runner.settle(cleanupContext, request, execution, stderr) == nil
}

func (runner *Runner) finishInterruptedWithRetry(
	request Request,
	execution string,
	exitCode int,
	startupRetries int,
	stderr io.Writer,
) Result {
	if exitCode != ExitInterrupted && exitCode != ExitTerminated && exitCode != ExitTimedOut {
		exitCode = ExitInterrupted
	}
	if execution != "" && !runner.settleFresh(request, execution, stderr) {
		exitCode = ExitSettlementFailed
		return runner.finishFailureWithRetry(
			request,
			Result{ExitCode: exitCode, Category: "settlement-unconfirmed", Execution: execution},
			"interrupted",
			execution,
			startupRetries,
			stderr,
		)
	}
	return runner.finishFailureWithRetry(
		request,
		Result{ExitCode: exitCode, Category: "interrupted", Execution: execution},
		"interrupted",
		execution,
		startupRetries,
		stderr,
	)
}

func (runner *Runner) finishFailureWithRetry(
	request Request,
	result Result,
	waitStatus, execution string,
	startupRetries int,
	stderr io.Writer,
) Result {
	if result.ExitCode == ExitSuccess {
		result.ExitCode = ExitControlPlane
	}
	if result.Execution == "" {
		result.Execution = execution
	}
	diagnosticsContext, cancel := context.WithTimeout(context.Background(), min(runner.limits.CleanupTimeout, 2*time.Minute))
	defer cancel()
	builder := newDiagnosticsBuilder(request)
	builder.readinessStatus(result.ExitCode, waitStatus)
	builder.backendStartupRetries(startupRetries)
	builder.identity(result.Execution)
	if result.Execution != "" {
		runner.collectDiagnostics(diagnosticsContext, request, result.Execution, builder)
	}
	result.Diagnostics = builder.build()
	if err := writeDiagnostics(request.DiagnosticsPath, result.Diagnostics); err != nil {
		result.ExitCode = ExitSettlementFailed
		result.Category = "diagnostics-write"
	}
	_, _ = stderr.Write(result.Diagnostics.Render())
	return result
}

func (runner *Runner) preflightFailureResult(request Request, failure runFailure, stderr io.Writer) Result {
	builder := newDiagnosticsBuilder(request)
	builder.preflight(failure.category, failure.observed, failure.maximum)
	result := Result{ExitCode: failure.exitCode, Category: failure.category, Diagnostics: builder.build()}
	if !request.PreflightOnly {
		if err := writeDiagnostics(request.DiagnosticsPath, result.Diagnostics); err != nil {
			result.ExitCode = ExitSettlementFailed
			result.Category = "diagnostics-write"
		}
		_, _ = stderr.Write(result.Diagnostics.Render())
	}
	return result
}

func runnerFailureResult(request Request, exitCode int, category, waitStatus, execution string, stderr io.Writer) Result {
	builder := newDiagnosticsBuilder(request)
	builder.readinessStatus(exitCode, waitStatus)
	builder.identity(execution)
	diagnostics := builder.build()
	if !request.PreflightOnly && request.DiagnosticsPath != "" {
		if err := writeDiagnostics(request.DiagnosticsPath, diagnostics); err != nil {
			exitCode = ExitSettlementFailed
			category = "diagnostics-write"
		}
		_, _ = stderr.Write(diagnostics.Render())
	}
	return Result{ExitCode: exitCode, Category: category, Execution: execution, Diagnostics: diagnostics}
}

func (runner *Runner) collectDiagnostics(ctx context.Context, request Request, execution string, builder *diagnosticsBuilder) {
	record, err := runner.client.DescribeExecution(ctx, request.Job, execution)
	if err != nil {
		builder.execution(Execution{}, "unavailable")
	} else if validated, validationErr := validateExecution(record, request.Project, request.Region, request.Job); validationErr != nil || validated.Name != execution {
		builder.execution(Execution{}, "invalid")
	} else {
		builder.execution(validated, "ok")
	}
	tasks, err := runner.client.ListTasks(ctx, request.Job, execution)
	if err != nil {
		builder.tasks(nil, "unavailable")
	} else if validated, validationErr := validateTasks(tasks); validationErr != nil {
		builder.tasks(nil, "invalid")
	} else {
		builder.tasks(validated, "ok")
	}
	markers, err := runner.client.ReadinessMarkers(ctx, request.Job, execution, request.Kind)
	if err != nil {
		builder.markers(request.Kind, nil, "unavailable")
	} else {
		builder.markers(request.Kind, markers, "ok")
	}
}

func (runner *Runner) executionMarker() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	random := make([]byte, 6)
	for index := range random {
		for {
			candidate := []byte{0}
			if _, err := io.ReadFull(runner.random, candidate); err != nil {
				return "", err
			}
			if candidate[0] < 248 {
				random[index] = alphabet[int(candidate[0])%len(alphabet)]
				break
			}
		}
	}
	marker := "readiness-" + strconv.Itoa(runner.pid) + "-" + string(random)
	if !markerPattern.MatchString(marker) {
		return "", errors.New("invalid execution marker")
	}
	return marker, nil
}

func (runner *Runner) executionMarkerExcluding(excluded map[string]struct{}) (string, error) {
	for attempt := 0; attempt < uniqueMarkerAttempts; attempt++ {
		marker, err := runner.executionMarker()
		if err != nil {
			return "", err
		}
		if _, exists := excluded[marker]; !exists {
			return marker, nil
		}
	}
	return "", errors.New("unique execution marker unavailable")
}

func (runner *Runner) recoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if !contextStopped(ctx) {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(context.Background(), runner.limits.CleanupTimeout)
}

func waitForTimer(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func contextStopped(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}

func contextExitCode(ctx context.Context) int {
	if ctx == nil {
		return ExitInterrupted
	}
	var signal signalCause
	if errors.As(context.Cause(ctx), &signal) {
		return signal.exitCode
	}
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) || errors.Is(context.Cause(ctx), errExecutionDeadline) {
		return ExitTimedOut
	}
	return ExitInterrupted
}

func waitContextFailure(parent context.Context) *runFailure {
	if contextStopped(parent) {
		if errors.Is(context.Cause(parent), errExecutionDeadline) {
			return &runFailure{exitCode: ExitTimedOut, category: "timeout", status: "timeout"}
		}
		return &runFailure{exitCode: contextExitCode(parent), category: "interrupted", status: "interrupted"}
	}
	return &runFailure{exitCode: ExitTimedOut, category: "timeout", status: "timeout"}
}

func waitStatusForIdentity(status string) string {
	if status == "invalid-identity" {
		return status
	}
	return "identity-unavailable"
}

func normalizeLimits(limits Limits) Limits {
	defaults := DefaultLimits()
	if limits.HistoryExecutions < 1 || limits.HistoryExecutions > 10_000 {
		limits.HistoryExecutions = defaults.HistoryExecutions
	}
	if limits.ActiveExecutions < 1 || limits.ActiveExecutions > limits.HistoryExecutions {
		limits.ActiveExecutions = defaults.ActiveExecutions
	}
	if limits.PreflightPasses < 1 || limits.PreflightPasses > 10 {
		limits.PreflightPasses = defaults.PreflightPasses
	}
	if limits.MarkerRecoveryAttempts < 1 || limits.MarkerRecoveryAttempts > 100 {
		limits.MarkerRecoveryAttempts = defaults.MarkerRecoveryAttempts
	}
	if limits.ConsecutiveQueryFailures < 1 || limits.ConsecutiveQueryFailures > 20 {
		limits.ConsecutiveQueryFailures = defaults.ConsecutiveQueryFailures
	}
	if limits.ExecutionPolls < 1 || limits.ExecutionPolls > 10_000 {
		limits.ExecutionPolls = defaults.ExecutionPolls
	}
	if limits.SettlementPolls < 1 || limits.SettlementPolls > 10_000 {
		limits.SettlementPolls = defaults.SettlementPolls
	}
	if limits.PollInterval <= 0 || limits.PollInterval > time.Minute {
		limits.PollInterval = defaults.PollInterval
	}
	if limits.ExecutionTimeout <= 0 || limits.ExecutionTimeout > 2*time.Hour {
		limits.ExecutionTimeout = defaults.ExecutionTimeout
	}
	if limits.SettlementTimeout <= 0 || limits.SettlementTimeout > 2*time.Hour {
		limits.SettlementTimeout = defaults.SettlementTimeout
	}
	if limits.CleanupTimeout < time.Second || limits.CleanupTimeout > 2*time.Hour {
		limits.CleanupTimeout = defaults.CleanupTimeout
	}
	return limits
}
