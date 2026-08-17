package cloudrunreadiness

import (
	"fmt"
	"strings"
	"testing"
)

func TestDecodeStrictJSONRejectsMalformedTrailingBytes(t *testing.T) {
	t.Parallel()
	data := []byte(`{"metadata":{"name":"scribe-prod-backend-readiness-abcde"}} trailing-secret`)
	if _, err := parseExecution(data, "scribe-test", "us-central1", "scribe-prod-backend-readiness"); err == nil {
		t.Fatal("parseExecution accepted malformed trailing bytes")
	}
}

func TestParseExecutionListRejectsNull(t *testing.T) {
	t.Parallel()
	if _, err := parseExecutionList([]byte("null"), "scribe-test", "us-central1", "scribe-prod-backend-readiness"); err == nil {
		t.Fatal("parseExecutionList accepted null as an empty execution list")
	}
	executions, err := parseExecutionList([]byte("[]"), "scribe-test", "us-central1", "scribe-prod-backend-readiness")
	if err != nil {
		t.Fatalf("parseExecutionList rejected an empty array: %v", err)
	}
	if len(executions) != 0 {
		t.Fatalf("empty array produced %d executions", len(executions))
	}
}

func TestTypedArrayParsersRejectNull(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		parse func([]byte) error
	}{
		{
			name: "tasks",
			parse: func(data []byte) error {
				_, err := parseTasks(data)
				return err
			},
		},
		{
			name: "logs",
			parse: func(data []byte) error {
				_, err := parseLogMarkers(data, "scribe-prod-backend-readiness-abcde", KindBackend)
				return err
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.parse([]byte("null")); err == nil {
				t.Fatal("parser accepted null as an empty array")
			}
			if err := test.parse([]byte("[]")); err != nil {
				t.Fatalf("parser rejected an empty array: %v", err)
			}
		})
	}
}

func TestParseExecutionListClassifiesTerminalHistoryWithoutCompletionTime(t *testing.T) {
	t.Parallel()
	const job = "scribe-prod-backend-readiness"
	var payload strings.Builder
	payload.WriteByte('[')
	for index := 0; index < 65; index++ {
		if index > 0 {
			payload.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&payload,
			`{"metadata":{"name":"%s-%05x"},"status":{"conditions":[{"type":"Completed","status":"True"}]}}`,
			job, index)
	}
	payload.WriteString(`,{"metadata":{"name":"` + job + `-zzzzz"},"status":{"conditions":[{"type":"Completed","status":"False","reason":"CANCELLING"}]}}]`)

	executions, err := parseExecutionList([]byte(payload.String()), "scribe-test", "us-central1", job)
	if err != nil {
		t.Fatalf("parseExecutionList: %v", err)
	}
	if len(executions) != 66 {
		t.Fatalf("execution count = %d, want 66", len(executions))
	}
	for index, execution := range executions[:65] {
		if execution.State != ExecutionSucceeded || execution.CompletionTime != "unknown" {
			t.Fatalf("terminal %d = %+v, want succeeded without completion time", index, execution)
		}
	}
	if executions[65].State != ExecutionRunning {
		t.Fatalf("CANCELLING execution state = %q, want running", executions[65].State)
	}
}

func TestParseExecutionFindsMarkerInLegacyTemplateLocation(t *testing.T) {
	t.Parallel()
	const marker = "readiness-42-AbCd09"
	data := []byte(`{
		"metadata":{"name":"scribe-prod-backend-readiness-abcde"},
		"status":{"conditions":[{"type":"Completed","status":"Unknown"}]},
		"spec":{"template":{"containers":[{"env":[
			{"name":"UNRELATED_SECRET","value":"never-retain-me"},
			{"name":"SCRIBE_READINESS_EXECUTION_ID","value":"` + marker + `"}
		]}]}}
	}`)
	execution, err := parseExecution(data, "scribe-test", "us-central1", "scribe-prod-backend-readiness")
	if err != nil {
		t.Fatalf("parseExecution: %v", err)
	}
	if execution.ReadinessMarker != marker {
		t.Fatalf("marker = %q, want %q", execution.ReadinessMarker, marker)
	}
	if strings.Contains(strings.Join([]string{execution.Name, execution.Reason, execution.ReadinessMarker}, " "), "never-retain-me") {
		t.Fatal("normalized model retained unrelated environment value")
	}
}

func TestParseExecutionAcceptsScopedNumericProjectResourceName(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"name":"projects/123456789012/locations/us-central1/jobs/scribe-prod-backend-readiness/executions/scribe-prod-backend-readiness-abcde",
		"conditions":[{"type":"Completed","state":"CONDITION_FAILED","reason":"NonZeroExitCode"}]
	}`)
	execution, err := parseExecution(data, "scribe-test", "us-central1", "scribe-prod-backend-readiness")
	if err != nil {
		t.Fatalf("parseExecution: %v", err)
	}
	if execution.Name != "scribe-prod-backend-readiness-abcde" {
		t.Fatalf("name = %q", execution.Name)
	}
	if execution.Reason != "non-zero-exit" {
		t.Fatalf("reason = %q, want non-zero-exit", execution.Reason)
	}
	builder := newDiagnosticsBuilder(Request{Job: "scribe-prod-backend-readiness", Kind: KindBackend})
	builder.execution(execution, "ok")
	if rendered := string(builder.build().Render()); !strings.Contains(rendered, "[execution] reason=non-zero-exit\n") {
		t.Fatalf("diagnostics = %q", rendered)
	}
}

func TestLaunchIdentityRejectsNumericProjectAndWhitespacePadding(t *testing.T) {
	t.Parallel()
	job := "scribe-prod-backend-readiness"
	if _, err := executionLeaf("scribe-test", "us-central1", job,
		"projects/123456789012/locations/us-central1/jobs/"+job+"/executions/"+job+"-abcde"); err == nil {
		t.Fatal("launch identity accepted a numeric project resource name")
	}
	if _, err := executionLeaf("scribe-test", "us-central1", job, " "+job+"-abcde"); err == nil {
		t.Fatal("launch identity accepted leading whitespace")
	}
}
