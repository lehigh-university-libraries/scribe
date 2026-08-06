package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/deployer"
)

func TestRunStatus(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"DEPLOY_MODE":       "apply",
		"PLAN_OUTCOME":      "success",
		"APPLY_OUTCOME":     "success",
		"REVISION_OUTCOME":  "success",
		"READINESS_OUTCOME": "failure",
		"ROLLBACK_OUTCOME":  "success",
	}
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"status"}, &stdout, dependencies{
		getenv: mapEnvironment(environment),
	}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got, want := stdout.String(), "readiness-failed-rolled-back\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunPreviewInputs(t *testing.T) {
	t.Parallel()

	const outputPath = "/runner/github-output"
	environment := map[string]string{
		"GITHUB_OUTPUT":               outputPath,
		"GITHUB_REPOSITORY":           "example/scribe",
		"GCLOUD_PROJECT":              "example-project",
		"SCRIBE_PREVIEW_MACHINE_TYPE": "n2d-standard-2",
		"SCRIBE_REGION":               "us-east5",
		"SCRIBE_ZONE":                 "us-east5-c",
		"WORKFLOW_REF":                "refs/heads/main",
		"DISPATCH_ACTION":             "recover-destroy",
		"DISPATCH_PR":                 "75",
	}
	github := &commandGitHub{
		mainSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		pullRequest: deployer.PullRequest{
			BaseRef:        "main",
			HeadRepository: "example/scribe",
			HeadSHA:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
	output := &writeCloser{}
	var openedPath string
	if err := run(context.Background(), []string{"preview-inputs"}, io.Discard, dependencies{
		getenv: mapEnvironment(environment),
		github: github,
		open: func(path string) (io.WriteCloser, error) {
			openedPath = path
			return output, nil
		},
	}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if openedPath != outputPath {
		t.Fatalf("opened path = %q, want %q", openedPath, outputPath)
	}
	if !output.closed {
		t.Fatal("GitHub output was not closed")
	}
	for _, line := range []string{"mode=destroy", "recover_destroy_inputs=true", "preview_machine_type=n2d-standard-2", "base_sha=" + github.mainSHA} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Errorf("GitHub output does not contain %q: %s", line, output.String())
		}
	}
}

func TestRunRuntimeOverrides(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"runtime-overrides"}, &stdout, dependencies{
		getenv: mapEnvironment(map[string]string{"STORAGE_RESERVATION_TTL": "2h"}),
	}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got, want := stdout.String(), "TF_VAR_storage_reservation_ttl=2h\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestOpenGitHubOutputRequiresExistingRegularAbsoluteFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "github-output")
	if err := os.WriteFile(path, []byte("existing=true\n"), 0o600); err != nil {
		t.Fatalf("seed output: %v", err)
	}
	output, err := openGitHubOutput(path)
	if err != nil {
		t.Fatalf("openGitHubOutput: %v", err)
	}
	if _, err := output.Write([]byte("next=true\n")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if got, want := string(contents), "existing=true\nnext=true\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}

	if _, err := openGitHubOutput("relative-output"); err == nil {
		t.Fatal("relative GITHUB_OUTPUT path was accepted")
	}
	if _, err := openGitHubOutput(filepath.Join(directory, "missing")); err == nil {
		t.Fatal("missing GITHUB_OUTPUT path was accepted")
	}
	symlink := filepath.Join(directory, "output-link")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatalf("create output symlink: %v", err)
	}
	if _, err := openGitHubOutput(symlink); err == nil {
		t.Fatal("symlink GITHUB_OUTPUT path was accepted")
	}
}

func TestRunFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		deps dependencies
		want string
	}{
		{name: "unknown command", args: []string{"deploy"}, deps: dependencies{getenv: mapEnvironment(nil)}, want: "usage"},
		{name: "missing output", args: []string{"preview-inputs"}, deps: dependencies{getenv: mapEnvironment(nil), github: &commandGitHub{}, open: func(string) (io.WriteCloser, error) { return nil, errors.New("unused") }}, want: "GITHUB_OUTPUT"},
		{name: "invalid status", args: []string{"status"}, deps: dependencies{getenv: mapEnvironment(map[string]string{"DEPLOY_MODE": "release"})}, want: "DEPLOY_MODE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			err := run(context.Background(), test.args, &stdout, test.deps)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

type commandGitHub struct {
	mainSHA     string
	mainErr     error
	pullRequest deployer.PullRequest
	pullErr     error
}

func (github *commandGitHub) MainSHA(context.Context, string) (string, error) {
	return github.mainSHA, github.mainErr
}

func (github *commandGitHub) PullRequest(context.Context, string, string) (deployer.PullRequest, error) {
	return github.pullRequest, github.pullErr
}

type writeCloser struct {
	bytes.Buffer
	closed bool
}

func (writer *writeCloser) Close() error {
	writer.closed = true
	return nil
}

func mapEnvironment(values map[string]string) func(string) string {
	return func(name string) string {
		return values[name]
	}
}
