package productionbrowserreadiness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRequestNormalizesRelativeDiagnostics(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	artifacts := filepath.Join(root, "artifacts")
	if err := os.Mkdir(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := testExecutable(t, root, "transport")
	readiness := testExecutable(t, root, "cloud-readiness")
	environment := validEnvironment(root, readiness)
	request, err := ParseRequest(
		[]string{"scribe-browser-acde1234", "scribe-browser-session-acde1234", "artifacts/readiness.log"},
		func(name string) string { return environment[name] },
		root,
		executable,
	)
	if err != nil {
		t.Fatalf("ParseRequest() error = %v", err)
	}
	if request.DiagnosticsPath != filepath.Join(root, "artifacts", "readiness.log") {
		t.Fatalf("DiagnosticsPath = %q", request.DiagnosticsPath)
	}
	if request.TransportExecutable != executable || request.CloudReadinessExecutable != readiness {
		t.Fatalf("executables not preserved: %+v", request)
	}
}

func TestParseRequestRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	artifacts := filepath.Join(root, "artifacts")
	if err := os.Mkdir(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := testExecutable(t, root, "transport")
	readiness := testExecutable(t, root, "cloud-readiness")
	base := validEnvironment(root, readiness)
	baseArgs := []string{"scribe-browser-acde1234", "scribe-browser-session-acde1234", "artifacts/readiness.log"}
	tests := []struct {
		name        string
		args        []string
		environment func(map[string]string)
		executable  string
	}{
		{name: "arguments", args: baseArgs[:2]},
		{name: "environment", args: baseArgs, environment: func(values map[string]string) { values[deploymentEnvironment] = "preview" }},
		{name: "project", args: baseArgs, environment: func(values map[string]string) { values[cloudProjectEnvironment] = "BAD" }},
		{name: "region", args: baseArgs, environment: func(values map[string]string) { values[regionEnvironment] = "us-east5;bad" }},
		{name: "zone mismatch", args: baseArgs, environment: func(values map[string]string) { values[zoneEnvironment] = "us-west1-b" }},
		{name: "instance", args: baseArgs, environment: func(values map[string]string) { values[instanceEnvironment] = "other" }},
		{name: "run id", args: baseArgs, environment: func(values map[string]string) { values[runIDEnvironment] = "01" }},
		{name: "attempt", args: baseArgs, environment: func(values map[string]string) { values[runAttemptEnvironment] = "0" }},
		{name: "job", args: []string{"scribe-pr-1-browser-acde1234", baseArgs[1], baseArgs[2]}},
		{name: "secret pair", args: []string{baseArgs[0], "scribe-browser-session-acde1235", baseArgs[2]}},
		{name: "diagnostics extension", args: []string{baseArgs[0], baseArgs[1], "artifacts/readiness.txt"}},
		{name: "relative executable", args: baseArgs, executable: "transport"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := cloneEnvironment(base)
			if test.environment != nil {
				test.environment(values)
			}
			candidate := executable
			if test.executable != "" {
				candidate = test.executable
			}
			_, err := ParseRequest(test.args, func(name string) string { return values[name] }, root, candidate)
			if err == nil || !IsValidationError(err) {
				t.Fatalf("ParseRequest() error = %v, want validation error", err)
			}
			if strings.Contains(err.Error(), values[cloudProjectEnvironment]) && values[cloudProjectEnvironment] == "BAD" {
				t.Fatalf("error leaked rejected value: %v", err)
			}
		})
	}
}

func TestValidateRequestRejectsSymlinkFiles(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	linked := filepath.Join(request.TemporaryRoot, "linked-readiness")
	if err := os.Symlink(request.CloudReadinessExecutable, linked); err != nil {
		t.Fatal(err)
	}
	request.CloudReadinessExecutable = linked
	if err := ValidateRequest(request); err == nil {
		t.Fatal("ValidateRequest() accepted symlink executable")
	}
}

func validEnvironment(root, readiness string) map[string]string {
	return map[string]string{
		cloudProjectEnvironment:      "scribe-test",
		deploymentEnvironment:        "production",
		regionEnvironment:            "us-east5",
		zoneEnvironment:              "us-east5-b",
		instanceEnvironment:          "scribe",
		runIDEnvironment:             "76543210",
		runAttemptEnvironment:        "3",
		temporaryRootEnvironment:     root,
		cloudReadinessBinEnvironment: readiness,
	}
}

func cloneEnvironment(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func validTestRequest(t *testing.T) Request {
	t.Helper()
	root := t.TempDir()
	artifacts := filepath.Join(root, "artifacts")
	if err := os.Mkdir(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	return Request{
		Project:                  "scribe-test",
		Region:                   "us-east5",
		Zone:                     "us-east5-b",
		Instance:                 "scribe",
		RunID:                    "76543210",
		RunAttempt:               "3",
		Job:                      "scribe-browser-acde1234",
		Secret:                   "scribe-browser-session-acde1234",
		DiagnosticsPath:          filepath.Join(artifacts, "readiness.log"),
		TemporaryRoot:            root,
		TransportExecutable:      testExecutable(t, root, "transport"),
		CloudReadinessExecutable: testExecutable(t, root, "cloud-readiness"),
	}
}

func testExecutable(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("test executable\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
