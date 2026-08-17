package cloudrunreadiness

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recordingCommandRunner struct {
	args   []string
	result commandResult
}

func TestGCloudOperationsUseExactScopedArgv(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	client, err := NewGCloudClient(GCloudConfig{Project: "scribe-test", Region: "us-central1", Executable: executable})
	if err != nil {
		t.Fatalf("NewGCloudClient: %v", err)
	}
	recorder := &recordingCommandRunner{}
	client.run = recorder
	const (
		job       = "scribe-browser-deadbeef"
		execution = "scribe-browser-deadbeef-abcde"
		marker    = "readiness-42-AbCd09"
	)

	recorder.result = commandResult{stdout: []byte(execution + "\n")}
	if _, err := client.Execute(context.Background(), job, map[string]string{
		"SCRIBE_READINESS_EXECUTION_ID": marker,
		browserStateDigestEnvironment:   strings.Repeat("a", 64),
		browserSecretVersionEnvironment: "27",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertCommandArgs(t, recorder.args, []string{
		"run", "jobs", "execute", job,
		"--project", "scribe-test",
		"--region", "us-central1",
		"--update-env-vars=" + browserSecretVersionEnvironment + "=27," + browserStateDigestEnvironment + "=" + strings.Repeat("a", 64) + ",SCRIBE_READINESS_EXECUTION_ID=" + marker,
		"--async",
		"--format=value(metadata.name)",
		"--quiet",
	})
	if strings.Contains(strings.Join(recorder.args, " "), "--wait") {
		t.Fatalf("Execute args contain --wait: %v", recorder.args)
	}

	recorder.result = commandResult{stdout: []byte(`{"metadata":{"name":"` + execution + `"},"status":{"conditions":[{"type":"Completed","status":"Unknown"}]}}`)}
	if _, err := client.DescribeExecution(context.Background(), job, execution); err != nil {
		t.Fatalf("DescribeExecution: %v", err)
	}
	assertCommandArgs(t, recorder.args, []string{
		"run", "jobs", "executions", "describe", execution,
		"--project", "scribe-test",
		"--region", "us-central1",
		"--format=json",
	})

	recorder.result = commandResult{}
	if err := client.CancelExecution(context.Background(), job, execution); err != nil {
		t.Fatalf("CancelExecution: %v", err)
	}
	assertCommandArgs(t, recorder.args, []string{
		"run", "jobs", "executions", "cancel", execution,
		"--project", "scribe-test",
		"--region", "us-central1",
		"--async",
		"--quiet",
	})

	recorder.result = commandResult{stdout: []byte("[]")}
	if _, err := client.ListTasks(context.Background(), job, execution); err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	assertCommandArgs(t, recorder.args, []string{
		"run", "jobs", "executions", "tasks", "list",
		"--execution", execution,
		"--project", "scribe-test",
		"--region", "us-central1",
		"--limit", "4",
		"--format=json",
	})

	recorder.result = commandResult{stdout: []byte("[]")}
	if _, err := client.ReadinessMarkers(context.Background(), job, execution, KindBrowser); err != nil {
		t.Fatalf("ReadinessMarkers: %v", err)
	}
	filter := `resource.type="cloud_run_job" AND resource.labels.job_name="` + job + `" AND resource.labels.location="us-central1" AND labels."run.googleapis.com/execution_name"="` + execution + `"`
	assertCommandArgs(t, recorder.args, []string{
		"logging", "read", filter,
		"--project", "scribe-test",
		"--freshness", "2h",
		"--order", "asc",
		"--limit", "100",
		"--format=json",
	})
}

func assertCommandArgs(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v\nwant %#v", got, want)
	}
}

func (runner *recordingCommandRunner) Run(_ context.Context, _ string, args []string, _, _ time.Duration, _ int) commandResult {
	runner.args = append([]string(nil), args...)
	return runner.result
}

func TestGCloudListUsesFullCompactHistoryWithoutCompletionFilter(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	client, err := NewGCloudClient(GCloudConfig{
		Project:    "scribe-test",
		Region:     "us-central1",
		Executable: executable,
	})
	if err != nil {
		t.Fatalf("NewGCloudClient: %v", err)
	}
	recorder := &recordingCommandRunner{result: commandResult{stdout: []byte("[]"), exitCode: 0}}
	client.run = recorder
	_, err = client.ListExecutions(context.Background(), "scribe-prod-backend-readiness", 2000)
	if err != nil {
		t.Fatalf("ListExecutions: %v", err)
	}
	joined := strings.Join(recorder.args, " ")
	if strings.Contains(joined, "--filter") || strings.Contains(joined, "completionTime:*") {
		t.Fatalf("list args retained completion-time filter: %s", joined)
	}
	if !strings.Contains(joined, "--limit 2001") {
		t.Fatalf("list args = %s, want +1 sentinel limit", joined)
	}
	if !strings.Contains(joined, "--format=json(") {
		t.Fatalf("list args = %s, want compact JSON projection", joined)
	}
}

func TestExecutePreservesMalformedWhitespaceForIdentityValidation(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	client, err := NewGCloudClient(GCloudConfig{Project: "scribe-test", Region: "us-central1", Executable: executable})
	if err != nil {
		t.Fatalf("NewGCloudClient: %v", err)
	}
	recorder := &recordingCommandRunner{result: commandResult{
		stdout:   []byte(" scribe-prod-backend-readiness-abcde\n"),
		exitCode: 0,
	}}
	client.run = recorder
	launch, err := client.Execute(context.Background(), "scribe-prod-backend-readiness", map[string]string{
		"SCRIBE_READINESS_EXECUTION_ID": "readiness-42-AbCd09",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if launch.Candidate != " scribe-prod-backend-readiness-abcde" {
		t.Fatalf("candidate = %q, whitespace was normalized", launch.Candidate)
	}
}

func TestBoundedBufferDiscardsBeyondLimitWithoutShortWrite(t *testing.T) {
	t.Parallel()
	buffer := newBoundedBuffer(4)
	written, err := buffer.Write([]byte("secret-payload"))
	if err != nil || written != len("secret-payload") {
		t.Fatalf("Write = %d, %v", written, err)
	}
	if got := buffer.String(); got != "secr" {
		t.Fatalf("buffer = %q, want bounded prefix", got)
	}
	if !buffer.exceeded {
		t.Fatal("overflow was not recorded")
	}
}
