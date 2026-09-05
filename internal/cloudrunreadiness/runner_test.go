package cloudrunreadiness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeClient struct {
	list      func(context.Context, string, int) ([]Execution, error)
	describe  func(context.Context, string, string) (Execution, error)
	execute   func(context.Context, string, map[string]string) (LaunchResult, error)
	cancel    func(context.Context, string, string) error
	tasks     func(context.Context, string, string) ([]Task, error)
	markers   func(context.Context, string, string, Kind) ([]string, error)
	cancelled []string
	listedMax []int
	described []string
}

func (client *fakeClient) ListExecutions(ctx context.Context, job string, maximum int) ([]Execution, error) {
	client.listedMax = append(client.listedMax, maximum)
	if client.list == nil {
		return nil, nil
	}
	return client.list(ctx, job, maximum)
}

func (client *fakeClient) DescribeExecution(ctx context.Context, job, execution string) (Execution, error) {
	client.described = append(client.described, execution)
	if client.describe == nil {
		return Execution{}, errors.New("describe unavailable")
	}
	return client.describe(ctx, job, execution)
}

func (client *fakeClient) Execute(ctx context.Context, job string, environment map[string]string) (LaunchResult, error) {
	if client.execute == nil {
		return LaunchResult{ExitCode: ExitControlPlane}, errors.New("execute unavailable")
	}
	return client.execute(ctx, job, environment)
}

func (client *fakeClient) CancelExecution(ctx context.Context, job, execution string) error {
	client.cancelled = append(client.cancelled, execution)
	if client.cancel == nil {
		return nil
	}
	return client.cancel(ctx, job, execution)
}

func (client *fakeClient) ListTasks(ctx context.Context, job, execution string) ([]Task, error) {
	if client.tasks == nil {
		return nil, nil
	}
	return client.tasks(ctx, job, execution)
}

func (client *fakeClient) ReadinessMarkers(ctx context.Context, job, execution string, kind Kind) ([]string, error) {
	if client.markers == nil {
		return nil, nil
	}
	return client.markers(ctx, job, execution, kind)
}

func TestPreflightClassifiesSixtyFiveTerminalRecordsBeforeActiveExecution(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, true)
	terminal := executionHistory(request.Job, 65, ExecutionSucceeded)
	active := testExecution(request.Job, 999, ExecutionRunning)
	listCalls := 0
	client := &fakeClient{
		list: func(context.Context, string, int) ([]Execution, error) {
			listCalls++
			if listCalls == 1 {
				return append(append([]Execution{}, terminal...), active), nil
			}
			return append(append([]Execution{}, terminal...), Execution{Name: active.Name, State: ExecutionCancelled}), nil
		},
		describe: func(context.Context, string, string) (Execution, error) {
			return Execution{Name: active.Name, State: ExecutionCancelled}, nil
		},
	}

	result := testRunner(client).Run(context.Background(), request, io.Discard, io.Discard)
	if result.ExitCode != ExitSuccess {
		t.Fatalf("exit code = %d (%s), want 0", result.ExitCode, result.Category)
	}
	if got, want := client.cancelled, []string{active.Name}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("cancelled = %v, want %v", got, want)
	}
	for _, maximum := range client.listedMax {
		if maximum != DefaultLimits().HistoryExecutions {
			t.Fatalf("list maximum = %d, want %d", maximum, DefaultLimits().HistoryExecutions)
		}
	}
}

func TestPreflightFailsClosedAboveSixteenActiveExecutions(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, true)
	client := &fakeClient{
		list: func(context.Context, string, int) ([]Execution, error) {
			return executionHistory(request.Job, 17, ExecutionRunning), nil
		},
	}
	var stderr bytes.Buffer

	result := testRunner(client).Run(context.Background(), request, io.Discard, &stderr)
	if result.ExitCode != ExitSettlementFailed || result.Category != "active-limit" {
		t.Fatalf("result = %+v, want exit 126 active-limit", result)
	}
	if len(client.cancelled) != 0 {
		t.Fatalf("cancelled %d executions after over-limit response", len(client.cancelled))
	}
	if !strings.Contains(stderr.String(), "observed=17 maximum=16") {
		t.Fatalf("stderr = %q, want bounded active count", stderr.String())
	}
}

func TestPreLaunchListCancellationPreservesContextExitCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		context  func() (context.Context, context.CancelFunc)
		exitCode int
	}{
		{
			name: "SIGINT",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancelCause(context.Background())
				cancel(SignalCause(ExitInterrupted))
				return ctx, func() {}
			},
			exitCode: ExitInterrupted,
		},
		{
			name: "SIGTERM",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancelCause(context.Background())
				cancel(SignalCause(ExitTerminated))
				return ctx, func() {}
			},
			exitCode: ExitTerminated,
		},
		{
			name: "deadline",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Unix(1, 0))
			},
			exitCode: ExitTimedOut,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := test.context()
			defer cancel()
			executeCalled := false
			client := &fakeClient{
				list: func(ctx context.Context, _ string, _ int) ([]Execution, error) {
					return nil, ctx.Err()
				},
				execute: func(context.Context, string, map[string]string) (LaunchResult, error) {
					executeCalled = true
					return LaunchResult{}, nil
				},
			}
			result := testRunner(client).Run(ctx, testRequest(t, KindBackend, false), io.Discard, io.Discard)
			if result.ExitCode != test.exitCode || result.Category != "interrupted" {
				t.Fatalf("result = %+v, want interrupted exit %d", result, test.exitCode)
			}
			if executeCalled {
				t.Fatal("execution started after preflight context stopped")
			}
		})
	}
}

func TestMarkerRecoveryFindsRapidTerminalExecutionAfterLargeHistory(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	history := executionHistory(request.Job, 65, ExecutionSucceeded)
	newExecution := Execution{Name: request.Job + "-zzzzz", State: ExecutionSucceeded}
	var marker string
	listCalls := 0
	client := &fakeClient{
		list: func(context.Context, string, int) ([]Execution, error) {
			listCalls++
			if listCalls == 1 {
				return history, nil
			}
			return append(append([]Execution{}, history...), newExecution), nil
		},
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{ExitCode: ExitControlPlane}, errors.New("launch response unavailable")
		},
		describe: func(_ context.Context, _ string, execution string) (Execution, error) {
			if execution != newExecution.Name {
				t.Fatalf("described historical execution %q during marker recovery", execution)
			}
			return Execution{Name: newExecution.Name, State: ExecutionSucceeded, ReadinessMarker: marker}, nil
		},
	}

	result := testRunner(client).Run(context.Background(), request, io.Discard, io.Discard)
	if result.ExitCode != ExitSuccess || result.Execution != newExecution.Name {
		t.Fatalf("result = %+v, want recovered success", result)
	}
	if len(client.described) < 2 {
		t.Fatalf("describe calls = %v, want recovery and terminal confirmation", client.described)
	}
	for _, execution := range client.described {
		if execution != newExecution.Name {
			t.Fatalf("described historical identity %q", execution)
		}
	}
}

func TestUnresolvedPostExecuteOwnershipFailsClosedWithoutCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		recovery  func(string, string) ([]Execution, func(context.Context, string, string) (Execution, error))
		candidate func(string) string
	}{
		{
			name: "empty recovery after bounded launch failure",
			recovery: func(_, _ string) ([]Execution, func(context.Context, string, string) (Execution, error)) {
				return nil, nil
			},
		},
		{
			name: "invalid recovery identity",
			recovery: func(_, _ string) ([]Execution, func(context.Context, string, string) (Execution, error)) {
				return []Execution{{Name: "unscoped-execution", State: ExecutionRunning}}, nil
			},
		},
		{
			name: "ambiguous markers",
			recovery: func(job, marker string) ([]Execution, func(context.Context, string, string) (Execution, error)) {
				first := testExecution(job, 700, ExecutionRunning)
				second := testExecution(job, 701, ExecutionRunning)
				return []Execution{first, second}, func(_ context.Context, _, execution string) (Execution, error) {
					return Execution{Name: execution, State: ExecutionRunning, ReadinessMarker: marker}, nil
				}
			},
		},
		{
			name: "one matching candidate plus one failed describe",
			recovery: func(job, marker string) ([]Execution, func(context.Context, string, string) (Execution, error)) {
				first := testExecution(job, 702, ExecutionRunning)
				second := testExecution(job, 703, ExecutionRunning)
				return []Execution{first, second}, func(_ context.Context, _, execution string) (Execution, error) {
					if execution == first.Name {
						return Execution{Name: execution, State: ExecutionRunning, ReadinessMarker: marker}, nil
					}
					return Execution{}, errors.New("candidate describe unavailable")
				}
			},
		},
		{
			name: "syntactic launch identity with wrong marker",
			candidate: func(job string) string {
				return testExecution(job, 704, ExecutionRunning).Name
			},
			recovery: func(job, _ string) ([]Execution, func(context.Context, string, string) (Execution, error)) {
				record := testExecution(job, 704, ExecutionRunning)
				return []Execution{record}, func(context.Context, string, string) (Execution, error) {
					record.ReadinessMarker = "readiness-99-ZZZZZZ"
					return record, nil
				}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := testRequest(t, KindBackend, false)
			marker := ""
			listCalls := 0
			var recoveryRecords []Execution
			var recoveryDescribe func(context.Context, string, string) (Execution, error)
			client := &fakeClient{
				list: func(context.Context, string, int) ([]Execution, error) {
					listCalls++
					if listCalls == 1 {
						return nil, nil
					}
					if recoveryRecords == nil && recoveryDescribe == nil {
						recoveryRecords, recoveryDescribe = test.recovery(request.Job, marker)
					}
					return recoveryRecords, nil
				},
				execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
					marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
					candidate := ""
					if test.candidate != nil {
						candidate = test.candidate(request.Job)
					}
					return LaunchResult{Candidate: candidate, ExitCode: 19}, errors.New("launch failed")
				},
				describe: func(ctx context.Context, job, execution string) (Execution, error) {
					if recoveryRecords == nil && recoveryDescribe == nil {
						recoveryRecords, recoveryDescribe = test.recovery(request.Job, marker)
					}
					if recoveryDescribe == nil {
						return Execution{}, errors.New("describe unavailable")
					}
					return recoveryDescribe(ctx, job, execution)
				},
			}
			limits := DefaultLimits()
			limits.MarkerRecoveryAttempts = 1
			result := NewRunner(client,
				WithLimits(limits),
				WithMarkerSource(bytes.NewReader([]byte{0, 1, 2, 3, 4, 5}), 42),
				WithWait(func(context.Context, time.Duration) error { return nil }),
			).Run(context.Background(), request, io.Discard, io.Discard)
			if result.ExitCode != ExitSettlementFailed || result.Category != "settlement-unconfirmed" {
				t.Fatalf("result = %+v, want unresolved ownership exit 126", result)
			}
			if len(client.cancelled) != 0 {
				t.Fatalf("cancelled unverified executions: %v", client.cancelled)
			}
		})
	}
}

func TestSignalDuringUnresolvedLaunchStillFailsClosed(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	ctx, cancel := context.WithCancelCause(context.Background())
	listCalls := 0
	client := &fakeClient{
		list: func(context.Context, string, int) ([]Execution, error) {
			listCalls++
			return nil, nil
		},
		execute: func(context.Context, string, map[string]string) (LaunchResult, error) {
			cancel(SignalCause(ExitTerminated))
			return LaunchResult{ExitCode: ExitTerminated}, errors.New("launch interrupted")
		},
	}
	limits := DefaultLimits()
	limits.MarkerRecoveryAttempts = 1

	result := NewRunner(client,
		WithLimits(limits),
		WithMarkerSource(bytes.NewReader([]byte{0, 1, 2, 3, 4, 5}), 42),
	).Run(ctx, request, io.Discard, io.Discard)
	if result.ExitCode != ExitSettlementFailed || result.Category != "settlement-unconfirmed" {
		t.Fatalf("result = %+v, want unresolved SIGTERM launch exit 126", result)
	}
	if len(client.cancelled) != 0 {
		t.Fatalf("cancelled unverified executions: %v", client.cancelled)
	}
}

func TestProductionBrowserReceivesOnlyExactExecutionOverrides(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBrowser, false)
	request.Job = "scribe-browser-deadbeef"
	request.BrowserExpectedSecretVersion = "27"
	request.BrowserExpectedStorageStateSHA256 = strings.Repeat("a", 64)
	execution := testExecution(request.Job, 8, ExecutionSucceeded)
	var received map[string]string
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			received = make(map[string]string, len(environment))
			for key, value := range environment {
				received[key] = value
			}
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(context.Context, string, string) (Execution, error) {
			return Execution{Name: execution.Name, State: ExecutionSucceeded, ReadinessMarker: received["SCRIBE_READINESS_EXECUTION_ID"]}, nil
		},
	}

	result := testRunner(client).Run(context.Background(), request, io.Discard, io.Discard)
	if result.ExitCode != ExitSuccess {
		t.Fatalf("result = %+v, want success", result)
	}
	if len(received) != 3 ||
		received[browserSecretVersionEnvironment] != request.BrowserExpectedSecretVersion ||
		received[browserStateDigestEnvironment] != request.BrowserExpectedStorageStateSHA256 ||
		!markerPattern.MatchString(received["SCRIBE_READINESS_EXECUTION_ID"]) {
		t.Fatalf("execution overrides = %#v", received)
	}
}

func TestProductionBackendStartupLagSucceedsOnSixthOwnedExecution(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	executions := make([]Execution, productionBackendExecutionAttempts)
	for index := range executions {
		executions[index] = testExecution(request.Job, 810+index, ExecutionFailed)
	}
	executions[len(executions)-1].State = ExecutionSucceeded
	markersByExecution := make(map[string]string)
	var launched []string
	var deadlines []time.Time
	client := &fakeClient{
		execute: func(ctx context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			index := len(launched)
			if index >= len(executions) {
				t.Fatal("started a seventh production backend execution")
			}
			marker := environment["SCRIBE_READINESS_EXECUTION_ID"]
			launched = append(launched, marker)
			markersByExecution[executions[index].Name] = marker
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("execution attempt had no overall deadline")
			}
			deadlines = append(deadlines, deadline)
			return LaunchResult{Candidate: executions[index].Name}, nil
		},
		describe: func(_ context.Context, _ string, execution string) (Execution, error) {
			for _, record := range executions {
				if record.Name == execution {
					record.ReadinessMarker = markersByExecution[execution]
					return record, nil
				}
			}
			return Execution{}, errors.New("unknown execution")
		},
		markers: func(_ context.Context, _ string, execution string, kind Kind) ([]string, error) {
			if execution == executions[len(executions)-1].Name || kind != KindBackend {
				t.Fatalf("retry evidence queried for %q/%q", execution, kind)
			}
			return backendStartupRetryMarkers(), nil
		},
	}
	var stdout bytes.Buffer
	result := retryTestRunner(client, DefaultLimits()).Run(context.Background(), request, &stdout, io.Discard)
	if result.ExitCode != ExitSuccess || result.Execution != executions[len(executions)-1].Name {
		t.Fatalf("result = %+v, want sixth execution success", result)
	}
	uniqueMarkers := make(map[string]struct{}, len(launched))
	for _, marker := range launched {
		if !markerPattern.MatchString(marker) {
			t.Fatalf("invalid execution marker %q", marker)
		}
		uniqueMarkers[marker] = struct{}{}
	}
	if len(launched) != productionBackendExecutionAttempts || len(uniqueMarkers) != len(launched) {
		t.Fatalf("execution markers = %v, want six unique valid markers", launched)
	}
	if len(deadlines) != productionBackendExecutionAttempts {
		t.Fatalf("execution deadlines = %v, want six", deadlines)
	}
	for _, deadline := range deadlines[1:] {
		if !deadlines[0].Equal(deadline) {
			t.Fatalf("execution deadlines = %v, want one shared overall deadline", deadlines)
		}
	}
	if got := strings.Count(stdout.String(), "Retrying Cloud Run backend readiness after confirmed guest-startup lag."); got != productionBackendExecutionAttempts-1 {
		t.Fatalf("stdout = %q, retry indication count = %d", stdout.String(), got)
	}
	if got := strings.Count(stdout.String(), "Running Cloud Run backend readiness job"); got != productionBackendExecutionAttempts {
		t.Fatalf("stdout = %q, execution indication count = %d", stdout.String(), got)
	}
	for _, marker := range launched {
		if strings.Contains(stdout.String(), marker) {
			t.Fatalf("stdout exposed private execution marker %q: %q", marker, stdout.String())
		}
	}
}

func TestBackendStartupMarkersRejectNonAdjacentCollisionDuringRecovery(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	executions := []Execution{
		testExecution(request.Job, 816, ExecutionFailed),
		testExecution(request.Job, 817, ExecutionFailed),
		testExecution(request.Job, 818, ExecutionSucceeded),
	}
	markersByExecution := make(map[string]string)
	var launched []string
	client := &fakeClient{
		list: func(context.Context, string, int) ([]Execution, error) {
			records := make([]Execution, len(launched))
			for index := range records {
				records[index] = executions[index]
				records[index].ReadinessMarker = markersByExecution[records[index].Name]
			}
			return records, nil
		},
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			index := len(launched)
			if index >= len(executions) {
				t.Fatal("started an unexpected backend execution")
			}
			marker := environment["SCRIBE_READINESS_EXECUTION_ID"]
			launched = append(launched, marker)
			markersByExecution[executions[index].Name] = marker
			if index == len(executions)-1 {
				return LaunchResult{}, nil
			}
			return LaunchResult{Candidate: executions[index].Name}, nil
		},
		describe: func(_ context.Context, _ string, execution string) (Execution, error) {
			for _, record := range executions {
				if record.Name == execution {
					record.ReadinessMarker = markersByExecution[execution]
					return record, nil
				}
			}
			return Execution{}, errors.New("unknown execution")
		},
		markers: func(_ context.Context, _ string, execution string, _ Kind) ([]string, error) {
			if execution == executions[len(executions)-1].Name {
				t.Fatal("successful execution queried for retry evidence")
			}
			return backendStartupRetryMarkers(), nil
		},
	}
	markerEntropy := []byte{
		0, 1, 2, 3, 4, 5,
		6, 7, 8, 9, 10, 11,
		0, 1, 2, 3, 4, 5,
		12, 13, 14, 15, 16, 17,
	}
	runner := NewRunner(client,
		WithMarkerSource(bytes.NewReader(markerEntropy), 42),
		WithWait(func(ctx context.Context, _ time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		}),
	)
	var stdout bytes.Buffer
	result := runner.Run(context.Background(), request, &stdout, io.Discard)
	if result.ExitCode != ExitSuccess || result.Execution != executions[2].Name {
		t.Fatalf("result = %+v, want recovered third execution success", result)
	}
	if len(launched) != 3 || launched[0] == launched[1] || launched[0] == launched[2] || launched[1] == launched[2] {
		t.Fatalf("execution markers = %v, want three distinct markers", launched)
	}
	for _, marker := range launched {
		if strings.Contains(stdout.String(), marker) {
			t.Fatalf("stdout exposed private execution marker %q: %q", marker, stdout.String())
		}
	}
}

func TestProductionBackendStartupLagStopsAfterSixExecutions(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	executions := make([]Execution, productionBackendExecutionAttempts)
	for index := range executions {
		executions[index] = testExecution(request.Job, 820+index, ExecutionFailed)
	}
	markersByExecution := make(map[string]string)
	executeCalls := 0
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			if executeCalls >= len(executions) {
				t.Fatal("started a seventh production backend execution")
			}
			execution := executions[executeCalls]
			executeCalls++
			markersByExecution[execution.Name] = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(_ context.Context, _ string, execution string) (Execution, error) {
			for _, record := range executions {
				if record.Name == execution {
					record.ReadinessMarker = markersByExecution[execution]
					return record, nil
				}
			}
			return Execution{}, errors.New("unknown execution")
		},
		markers: func(context.Context, string, string, Kind) ([]string, error) {
			return backendStartupRetryMarkers(), nil
		},
	}
	var stdout bytes.Buffer
	result := retryTestRunner(client, DefaultLimits()).Run(context.Background(), request, &stdout, io.Discard)
	if result.ExitCode != ExitReadinessFailed || result.Category != "terminal-failure" || result.Execution != executions[len(executions)-1].Name {
		t.Fatalf("result = %+v, want sixth terminal failure", result)
	}
	if executeCalls != productionBackendExecutionAttempts {
		t.Fatalf("execute calls = %d, want exactly six", executeCalls)
	}
	if !strings.Contains(string(result.Diagnostics.Render()), "[status] backend_startup_retries=5") {
		t.Fatalf("diagnostics = %q, want bounded retry count", result.Diagnostics.Render())
	}
	if got := strings.Count(stdout.String(), "Retrying Cloud Run backend readiness after confirmed guest-startup lag."); got != productionBackendExecutionAttempts-1 {
		t.Fatalf("stdout = %q, retry indication count = %d", stdout.String(), got)
	}
}

func TestPreviewBackendStartupLagStopsAfterTwoExecutions(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	request.Job = "scribe-pr-7-pr-7-backend-readiness"
	executions := []Execution{
		testExecution(request.Job, 830, ExecutionFailed),
		testExecution(request.Job, 831, ExecutionFailed),
	}
	markersByExecution := make(map[string]string)
	executeCalls := 0
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			if executeCalls >= len(executions) {
				t.Fatal("started a third preview backend execution")
			}
			execution := executions[executeCalls]
			executeCalls++
			markersByExecution[execution.Name] = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(_ context.Context, _ string, execution string) (Execution, error) {
			for _, record := range executions {
				if record.Name == execution {
					record.ReadinessMarker = markersByExecution[execution]
					return record, nil
				}
			}
			return Execution{}, errors.New("unknown execution")
		},
		markers: func(context.Context, string, string, Kind) ([]string, error) {
			return backendStartupRetryMarkers(), nil
		},
	}
	result := retryTestRunner(client, DefaultLimits()).Run(context.Background(), request, io.Discard, io.Discard)
	if result.ExitCode != ExitReadinessFailed || result.Execution != executions[1].Name || executeCalls != previewBackendExecutionAttempts {
		t.Fatalf("result = %+v; execute calls = %d, want two preview attempts", result, executeCalls)
	}
	if !strings.Contains(string(result.Diagnostics.Render()), "[status] backend_startup_retries=1") {
		t.Fatalf("diagnostics = %q, want one preview retry", result.Diagnostics.Render())
	}
}

func TestProductionBackendStartupRetryStopsWhenLaterFailureDoesNotRequalify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		nonqualifyingIndex int
	}{
		{name: "second execution", nonqualifyingIndex: 1},
		{name: "third execution", nonqualifyingIndex: 2},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := testRequest(t, KindBackend, false)
			executions := make([]Execution, test.nonqualifyingIndex+1)
			for index := range executions {
				executions[index] = testExecution(request.Job, 840+index, ExecutionFailed)
			}
			markersByExecution := make(map[string]string)
			executeCalls := 0
			client := &fakeClient{
				execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
					if executeCalls >= len(executions) {
						t.Fatal("started an execution after nonqualifying evidence")
					}
					execution := executions[executeCalls]
					executeCalls++
					markersByExecution[execution.Name] = environment["SCRIBE_READINESS_EXECUTION_ID"]
					return LaunchResult{Candidate: execution.Name}, nil
				},
				describe: func(_ context.Context, _ string, execution string) (Execution, error) {
					for _, record := range executions {
						if record.Name == execution {
							record.ReadinessMarker = markersByExecution[execution]
							return record, nil
						}
					}
					return Execution{}, errors.New("unknown execution")
				},
				markers: func(_ context.Context, _ string, execution string, _ Kind) ([]string, error) {
					if execution == executions[test.nonqualifyingIndex].Name {
						return []string{"frontend readiness failed: HTTP 503 (invalid-json)"}, nil
					}
					return backendStartupRetryMarkers(), nil
				},
			}
			result := retryTestRunner(client, DefaultLimits()).Run(context.Background(), request, io.Discard, io.Discard)
			if result.ExitCode != ExitReadinessFailed || result.Execution != executions[test.nonqualifyingIndex].Name || executeCalls != len(executions) {
				t.Fatalf("result = %+v; execute calls = %d", result, executeCalls)
			}
			wantRetries := fmt.Sprintf("[status] backend_startup_retries=%d", test.nonqualifyingIndex)
			if !strings.Contains(string(result.Diagnostics.Render()), wantRetries) {
				t.Fatalf("diagnostics = %q, want %q", result.Diagnostics.Render(), wantRetries)
			}
		})
	}
}

func TestBackendStartupRetryRejectsNonqualifyingMarkersAndKinds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    Kind
		state   ExecutionState
		markers []string
	}{
		{
			name:  "readiness contract",
			kind:  KindBackend,
			state: ExecutionFailed,
			markers: []string{
				"frontend backend startup gate failed [Error; readiness-contract; HTTP 503 (invalid-payload)]",
				backendFrontendRetryMarker,
				backendNetworkRetryMarker,
			},
		},
		{name: "missing network evidence", kind: KindBackend, state: ExecutionFailed, markers: []string{backendStartupGateRetryMarker, backendFrontendRetryMarker}},
		{name: "duplicate marker", kind: KindBackend, state: ExecutionFailed, markers: []string{backendStartupGateRetryMarker, backendFrontendRetryMarker, backendFrontendRetryMarker}},
		{name: "terminal cancellation", kind: KindBackend, state: ExecutionCancelled, markers: backendStartupRetryMarkers()},
		{name: "ocr", kind: KindOCR, state: ExecutionFailed, markers: backendStartupRetryMarkers()},
		{name: "browser", kind: KindBrowser, state: ExecutionFailed, markers: backendStartupRetryMarkers()},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := testRequest(t, test.kind, false)
			execution := testExecution(request.Job, 830, test.state)
			executeCalls := 0
			marker := ""
			client := &fakeClient{
				execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
					executeCalls++
					marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
					return LaunchResult{Candidate: execution.Name}, nil
				},
				describe: func(context.Context, string, string) (Execution, error) {
					record := execution
					record.ReadinessMarker = marker
					return record, nil
				},
				markers: func(context.Context, string, string, Kind) ([]string, error) {
					return test.markers, nil
				},
			}
			result := retryTestRunner(client, DefaultLimits()).Run(context.Background(), request, io.Discard, io.Discard)
			if result.ExitCode != ExitReadinessFailed || executeCalls != 1 {
				t.Fatalf("result = %+v; execute calls = %d, want one terminal attempt", result, executeCalls)
			}
		})
	}
}

func TestBackendStartupRetryRecoversTransientMarkerQuery(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	request.Job = "scribe-pr-7-pr-7-backend-readiness"
	executions := []Execution{
		testExecution(request.Job, 840, ExecutionFailed),
		testExecution(request.Job, 841, ExecutionSucceeded),
	}
	executeCalls := 0
	markerCalls := 0
	waits := 0
	markersByExecution := make(map[string]string)
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			execution := executions[executeCalls]
			executeCalls++
			markersByExecution[execution.Name] = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(_ context.Context, _ string, execution string) (Execution, error) {
			for _, record := range executions {
				if record.Name == execution {
					record.ReadinessMarker = markersByExecution[execution]
					return record, nil
				}
			}
			return Execution{}, errors.New("unknown execution")
		},
		markers: func(_ context.Context, _ string, execution string, _ Kind) ([]string, error) {
			if execution != executions[0].Name {
				t.Fatal("successful execution queried for retry evidence")
			}
			markerCalls++
			if markerCalls == 1 {
				return nil, errors.New("marker query unavailable")
			}
			if markerCalls == 2 {
				return backendStartupRetryMarkers()[:2], nil
			}
			return backendStartupRetryMarkers(), nil
		},
	}
	runner := retryTestRunner(client, DefaultLimits())
	runner.wait = func(ctx context.Context, _ time.Duration) error {
		waits++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	result := runner.Run(context.Background(), request, io.Discard, io.Discard)
	if result.ExitCode != ExitSuccess || result.Execution != executions[1].Name || executeCalls != 2 {
		t.Fatalf("result = %+v; execute calls = %d, want recovered retry success", result, executeCalls)
	}
	if markerCalls != 3 || waits != 2 {
		t.Fatalf("marker calls = %d; waits = %d, want three bounded queries and two waits", markerCalls, waits)
	}
}

func TestPartialBackendStartupRetryMarkers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		markers []string
		want    bool
	}{
		{name: "none yet", want: true},
		{name: "one known", markers: backendStartupRetryMarkers()[:1], want: true},
		{name: "two unique known", markers: backendStartupRetryMarkers()[:2], want: true},
		{name: "complete", markers: backendStartupRetryMarkers(), want: false},
		{name: "unknown", markers: []string{"unknown"}, want: false},
		{name: "duplicate", markers: []string{backendFrontendRetryMarker, backendFrontendRetryMarker}, want: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := partialBackendStartupRetryMarkers(test.markers); got != test.want {
				t.Fatalf("partialBackendStartupRetryMarkers(%q) = %t, want %t", test.markers, got, test.want)
			}
		})
	}
}

func TestBackendStartupRetryRequiresMarkersWithinRecoveryBound(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	execution := testExecution(request.Job, 840, ExecutionFailed)
	executeCalls := 0
	markerCalls := 0
	waits := 0
	marker := ""
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			executeCalls++
			marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(context.Context, string, string) (Execution, error) {
			record := execution
			record.ReadinessMarker = marker
			return record, nil
		},
		markers: func(context.Context, string, string, Kind) ([]string, error) {
			markerCalls++
			return nil, errors.New("marker query unavailable")
		},
	}
	limits := DefaultLimits()
	limits.MarkerRecoveryAttempts = 3
	runner := retryTestRunner(client, limits)
	runner.wait = func(ctx context.Context, _ time.Duration) error {
		waits++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	result := runner.Run(context.Background(), request, io.Discard, io.Discard)
	if result.ExitCode != ExitReadinessFailed || executeCalls != 1 {
		t.Fatalf("result = %+v; execute calls = %d, want no retry", result, executeCalls)
	}
	if markerCalls != limits.MarkerRecoveryAttempts+1 || waits != limits.MarkerRecoveryAttempts-1 {
		t.Fatalf("marker calls = %d; waits = %d, want bounded recovery plus diagnostics", markerCalls, waits)
	}
	if !strings.Contains(string(result.Diagnostics.Render()), "[status] log_query=unavailable") ||
		strings.Contains(string(result.Diagnostics.Render()), "backend_startup_retries") {
		t.Fatalf("diagnostics = %q", result.Diagnostics.Render())
	}
}

func TestBackendStartupRetryStopsOnCancellation(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	execution := testExecution(request.Job, 850, ExecutionFailed)
	ctx, cancel := context.WithCancelCause(context.Background())
	marker := ""
	executeCalls := 0
	markerCalls := 0
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			executeCalls++
			marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(context.Context, string, string) (Execution, error) {
			record := execution
			record.ReadinessMarker = marker
			return record, nil
		},
		markers: func(context.Context, string, string, Kind) ([]string, error) {
			markerCalls++
			if markerCalls == 1 {
				cancel(SignalCause(ExitTerminated))
			}
			return backendStartupRetryMarkers(), nil
		},
	}
	var stdout bytes.Buffer
	result := retryTestRunner(client, DefaultLimits()).Run(ctx, request, &stdout, io.Discard)
	if result.ExitCode != ExitTerminated || result.Category != "interrupted" || executeCalls != 1 {
		t.Fatalf("result = %+v; execute calls = %d, want one interrupted attempt", result, executeCalls)
	}
	if strings.Contains(stdout.String(), "Retrying Cloud Run backend readiness") {
		t.Fatalf("stdout announced a retry after cancellation: %q", stdout.String())
	}
}

func TestBackendStartupRetrySharesOneWallClockExecutionBound(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	executions := []Execution{
		testExecution(request.Job, 860, ExecutionFailed),
		testExecution(request.Job, 861, ExecutionRunning),
	}
	markersByExecution := make(map[string]string)
	describeCalls := make(map[string]int)
	var deadlines []time.Time
	executeCalls := 0
	client := &fakeClient{
		execute: func(ctx context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			if executeCalls >= len(executions) {
				t.Fatal("started more than one backend retry")
			}
			execution := executions[executeCalls]
			executeCalls++
			markersByExecution[execution.Name] = environment["SCRIBE_READINESS_EXECUTION_ID"]
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("execution attempt had no overall deadline")
			}
			deadlines = append(deadlines, deadline)
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(ctx context.Context, _ string, execution string) (Execution, error) {
			describeCalls[execution]++
			if execution == executions[0].Name {
				record := executions[0]
				record.ReadinessMarker = markersByExecution[execution]
				return record, nil
			}
			if describeCalls[execution] == 1 {
				record := executions[1]
				record.ReadinessMarker = markersByExecution[execution]
				return record, nil
			}
			if describeCalls[execution] == 2 {
				<-ctx.Done()
				return Execution{}, ctx.Err()
			}
			return Execution{Name: execution, State: ExecutionCancelled}, nil
		},
		markers: func(_ context.Context, _ string, execution string, _ Kind) ([]string, error) {
			if execution == executions[0].Name {
				return backendStartupRetryMarkers(), nil
			}
			return nil, nil
		},
	}
	limits := DefaultLimits()
	limits.ExecutionTimeout = 250 * time.Millisecond
	limits.ExecutionPolls = 100
	limits.SettlementPolls = 1
	started := time.Now()
	result := retryTestRunner(client, limits).Run(context.Background(), request, io.Discard, io.Discard)
	if result.ExitCode != ExitTimedOut || result.Category != "timeout" || executeCalls != 2 {
		t.Fatalf("result = %+v; execute calls = %d, want bounded retry timeout", result, executeCalls)
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("execution deadlines = %v, want one shared overall deadline", deadlines)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shared execution timeout took %v", elapsed)
	}
	if !strings.Contains(string(result.Diagnostics.Render()), "[status] backend_startup_retries=1") {
		t.Fatalf("diagnostics = %q, want bounded retry count", result.Diagnostics.Render())
	}
}

func TestOptionalDiagnosticQueryFailuresDoNotMaskReadinessFailure(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	execution := testExecution(request.Job, 9, ExecutionFailed)
	marker := ""
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(context.Context, string, string) (Execution, error) {
			record := execution
			record.ReadinessMarker = marker
			return record, nil
		},
		tasks: func(context.Context, string, string) ([]Task, error) {
			return nil, errors.New("task query failed")
		},
		markers: func(context.Context, string, string, Kind) ([]string, error) {
			return nil, errors.New("log query failed")
		},
	}

	result := testRunner(client).Run(context.Background(), request, io.Discard, io.Discard)
	if result.ExitCode != ExitReadinessFailed || result.Category != "terminal-failure" {
		t.Fatalf("result = %+v, want terminal readiness failure", result)
	}
	rendered := string(result.Diagnostics.Render())
	if !strings.Contains(rendered, "[status] task_query=unavailable") || !strings.Contains(rendered, "[status] log_query=unavailable") {
		t.Fatalf("diagnostics = %q", rendered)
	}
}

func TestFailedRunSettlesBackendByCancellationAndBrowserNaturally(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind        Kind
		wantCancels int
		terminal    ExecutionState
	}{
		{kind: KindBackend, wantCancels: 1, terminal: ExecutionCancelled},
		{kind: KindBrowser, wantCancels: 0, terminal: ExecutionSucceeded},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.kind), func(t *testing.T) {
			t.Parallel()
			request := testRequest(t, test.kind, false)
			execution := testExecution(request.Job, 1, ExecutionRunning)
			describeCalls := 0
			marker := ""
			client := &fakeClient{
				execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
					marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
					return LaunchResult{Candidate: execution.Name, ExitCode: 0}, nil
				},
				describe: func(context.Context, string, string) (Execution, error) {
					describeCalls++
					if describeCalls <= 2 {
						record := execution
						record.ReadinessMarker = marker
						return record, nil
					}
					return Execution{Name: execution.Name, State: test.terminal}, nil
				},
			}

			result := testRunnerWithPolls(client, 1, 2).Run(context.Background(), request, io.Discard, io.Discard)
			if result.ExitCode != ExitTimedOut || result.Category != "timeout" {
				t.Fatalf("result = %+v, want timeout preserved after settlement", result)
			}
			if got := len(client.cancelled); got != test.wantCancels {
				t.Fatalf("cancel calls = %d, want %d", got, test.wantCancels)
			}
		})
	}
}

func TestConsecutiveQueryFailuresSettleAndReturnControlPlaneStatus(t *testing.T) {
	t.Parallel()
	const sensitiveError = "adapter failed with TOP-SECRET-CONTENT"
	request := testRequest(t, KindBackend, false)
	execution := testExecution(request.Job, 2, ExecutionRunning)
	describeCalls := 0
	executeCalls := 0
	marker := ""
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			executeCalls++
			marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(context.Context, string, string) (Execution, error) {
			describeCalls++
			if describeCalls == 1 {
				record := execution
				record.ReadinessMarker = marker
				return record, nil
			}
			if describeCalls <= 4 {
				return Execution{}, errors.New(sensitiveError)
			}
			return Execution{Name: execution.Name, State: ExecutionCancelled}, nil
		},
	}
	var stderr bytes.Buffer

	result := testRunnerWithPolls(client, 5, 2).Run(context.Background(), request, io.Discard, &stderr)
	if result.ExitCode != ExitControlPlane || result.Category != "control-plane-unavailable" {
		t.Fatalf("result = %+v, want exit 125", result)
	}
	if len(client.cancelled) != 1 {
		t.Fatalf("cancel calls = %d, want 1", len(client.cancelled))
	}
	if executeCalls != 1 {
		t.Fatalf("execute calls = %d, want no control-plane retry", executeCalls)
	}
	if strings.Contains(stderr.String(), sensitiveError) {
		t.Fatalf("stderr leaked adapter error: %q", stderr.String())
	}
}

func TestSignalExitCodesSurviveOwnedExecutionSettlement(t *testing.T) {
	t.Parallel()
	for _, exitCode := range []int{ExitInterrupted, ExitTerminated} {
		exitCode := exitCode
		t.Run(fmt.Sprintf("exit-%d", exitCode), func(t *testing.T) {
			t.Parallel()
			request := testRequest(t, KindBackend, false)
			execution := testExecution(request.Job, exitCode, ExecutionRunning)
			ctx, cancel := context.WithCancelCause(context.Background())
			describeCalls := 0
			marker := ""
			client := &fakeClient{
				execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
					marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
					return LaunchResult{Candidate: execution.Name}, nil
				},
				describe: func(context.Context, string, string) (Execution, error) {
					describeCalls++
					if describeCalls == 1 {
						record := execution
						record.ReadinessMarker = marker
						return record, nil
					}
					if describeCalls == 2 {
						cancel(SignalCause(exitCode))
						return execution, nil
					}
					return Execution{Name: execution.Name, State: ExecutionCancelled}, nil
				},
			}

			result := testRunnerWithPolls(client, 3, 1).Run(ctx, request, io.Discard, io.Discard)
			if result.ExitCode != exitCode || result.Category != "interrupted" {
				t.Fatalf("result = %+v, want exit %d interrupted", result, exitCode)
			}
			if len(client.cancelled) != 1 {
				t.Fatalf("cancel calls = %d, want 1", len(client.cancelled))
			}
		})
	}
}

func TestSignalCleanupFailureOverridesWithSettlementExit(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	execution := testExecution(request.Job, 3, ExecutionRunning)
	ctx, cancel := context.WithCancelCause(context.Background())
	describeCalls := 0
	marker := ""
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(context.Context, string, string) (Execution, error) {
			describeCalls++
			if describeCalls == 1 {
				record := execution
				record.ReadinessMarker = marker
				return record, nil
			}
			if describeCalls == 2 {
				cancel(SignalCause(ExitTerminated))
			}
			return execution, nil
		},
	}

	result := testRunnerWithPolls(client, 3, 1).Run(ctx, request, io.Discard, io.Discard)
	if result.ExitCode != ExitSettlementFailed || result.Category != "settlement-unconfirmed" {
		t.Fatalf("result = %+v, want cleanup override 126", result)
	}
}

func TestWallClockTimeoutIncludesBlockingControlPlaneCall(t *testing.T) {
	t.Parallel()
	request := testRequest(t, KindBackend, false)
	execution := testExecution(request.Job, 5, ExecutionRunning)
	describeCalls := 0
	marker := ""
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(ctx context.Context, _ string, _ string) (Execution, error) {
			describeCalls++
			if describeCalls == 1 {
				record := execution
				record.ReadinessMarker = marker
				return record, nil
			}
			if describeCalls == 2 {
				<-ctx.Done()
				return Execution{}, ctx.Err()
			}
			return Execution{Name: execution.Name, State: ExecutionCancelled}, nil
		},
	}
	limits := DefaultLimits()
	limits.ExecutionTimeout = 10 * time.Millisecond
	limits.ExecutionPolls = 100
	limits.SettlementPolls = 1
	started := time.Now()

	result := NewRunner(client, WithLimits(limits), WithMarkerSource(bytes.NewReader([]byte{0, 1, 2, 3, 4, 5}), 42)).Run(
		context.Background(), request, io.Discard, io.Discard)
	if result.ExitCode != ExitTimedOut || result.Category != "timeout" {
		t.Fatalf("result = %+v, want wall-clock timeout", result)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("wall-clock timeout took %v", elapsed)
	}
}

func TestDiagnosticsNeverExposeAdapterOrLogSecrets(t *testing.T) {
	t.Parallel()
	const secret = "DO-NOT-LEAK-THIS-SECRET"
	request := testRequest(t, KindOCR, false)
	execution := testExecution(request.Job, 4, ExecutionFailed)
	marker := ""
	client := &fakeClient{
		execute: func(_ context.Context, _ string, environment map[string]string) (LaunchResult, error) {
			marker = environment["SCRIBE_READINESS_EXECUTION_ID"]
			return LaunchResult{Candidate: execution.Name}, nil
		},
		describe: func(context.Context, string, string) (Execution, error) {
			record := execution
			record.Reason = secret
			record.ReadinessMarker = marker
			return record, nil
		},
		tasks: func(context.Context, string, string) ([]Task, error) {
			return nil, errors.New(secret)
		},
		markers: func(context.Context, string, string, Kind) ([]string, error) {
			return []string{
				"ocr readiness failed: segment-timeout",
				secret,
				"ocr readiness failed: token " + secret,
			}, nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	result := testRunner(client).Run(context.Background(), request, &stdout, &stderr)
	data, err := os.ReadFile(request.DiagnosticsPath)
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	combined := stdout.String() + stderr.String() + string(data) + string(result.Diagnostics.Render())
	if strings.Contains(combined, secret) {
		t.Fatalf("diagnostics or streams leaked secret: %q", combined)
	}
	if !strings.Contains(combined, "ocr readiness failed: segment-timeout") {
		t.Fatalf("allowlisted marker missing: %q", combined)
	}
	if result.ExitCode != ExitReadinessFailed {
		t.Fatalf("exit code = %d, want 1", result.ExitCode)
	}
}

func testRunner(client Client) *Runner {
	return NewRunner(client,
		WithMarkerSource(bytes.NewReader([]byte{0, 1, 2, 3, 4, 5}), 42),
		WithWait(func(ctx context.Context, _ time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		}),
	)
}

func testRunnerWithPolls(client Client, executionPolls, settlementPolls int) *Runner {
	limits := DefaultLimits()
	limits.ExecutionPolls = executionPolls
	limits.SettlementPolls = settlementPolls
	return NewRunner(client,
		WithLimits(limits),
		WithMarkerSource(bytes.NewReader([]byte{0, 1, 2, 3, 4, 5}), 42),
		WithWait(func(ctx context.Context, _ time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		}),
	)
}

func retryTestRunner(client Client, limits Limits) *Runner {
	markerBytes := []byte{0, 1, 2, 3, 4, 5, 0, 1, 2, 3, 4, 5}
	for value := 6; value < 64; value++ {
		markerBytes = append(markerBytes, byte(value))
	}
	return NewRunner(client,
		WithLimits(limits),
		WithMarkerSource(bytes.NewReader(markerBytes), 42),
		WithWait(func(ctx context.Context, _ time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		}),
	)
}

func backendStartupRetryMarkers() []string {
	return []string{
		"frontend backend startup gate failed [Error; startup-deadline; transport-AbortError]",
		"frontend readiness failed: transport-AbortError",
		"frontend backend network probe [dns-match; tcp-timeout; http-timeout]",
	}
}

func testRequest(t *testing.T, kind Kind, preflight bool) Request {
	t.Helper()
	request := Request{
		Project:       "scribe-test",
		Region:        "us-central1",
		Kind:          kind,
		PreflightOnly: preflight,
	}
	switch kind {
	case KindBackend:
		request.Job = "scribe-prod-backend-readiness"
	case KindOCR:
		request.Job = "scribe-prod-ocr-readiness"
	case KindBrowser:
		request.Job = "scribe-pr-7-browser-deadbeef"
	}
	if !preflight {
		request.DiagnosticsPath = t.TempDir() + "/readiness.log"
	}
	return request
}

func testExecution(job string, index int, state ExecutionState) Execution {
	return Execution{Name: fmt.Sprintf("%s-%05x", job, index%0x100000), State: state}
}

func executionHistory(job string, count int, state ExecutionState) []Execution {
	executions := make([]Execution, 0, count)
	for index := 0; index < count; index++ {
		executions = append(executions, testExecution(job, index, state))
	}
	return executions
}
