package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/productionbrowserreadiness"
)

func TestRunDispatchesValidatedControllerRequest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	artifacts := filepath.Join(root, "artifacts")
	if err := os.Mkdir(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := commandTestExecutable(t, root, "production-browser-readiness")
	cloudReadiness := commandTestExecutable(t, root, "cloud-run-readiness")
	environment := commandTestEnvironment(root, cloudReadiness)
	var captured productionbrowserreadiness.Request
	deps := dependencies{
		getenv:     func(name string) string { return environment[name] },
		getwd:      func() (string, error) { return root, nil },
		executable: func() (string, error) { return executable, nil },
		newClient: func(productionbrowserreadiness.GCloudConfig) (productionbrowserreadiness.TransportClient, error) {
			return nil, nil
		},
		run: func(_ context.Context, _ productionbrowserreadiness.TransportClient, request productionbrowserreadiness.Request, _, _ io.Writer) productionbrowserreadiness.Result {
			captured = request
			return productionbrowserreadiness.Result{ExitCode: 37}
		},
		remote: func(context.Context, []string, string, io.Writer, io.Writer) int { return 99 },
	}
	status := run(context.Background(), []string{
		"scribe-browser-acde1234",
		"scribe-browser-session-acde1234",
		"artifacts/readiness.log",
	}, &bytes.Buffer{}, &bytes.Buffer{}, deps)
	if status != 37 {
		t.Fatalf("run() status = %d", status)
	}
	if captured.DiagnosticsPath != filepath.Join(artifacts, "readiness.log") || captured.TransportExecutable != executable {
		t.Fatalf("captured request = %+v", captured)
	}
}

func TestRunDispatchesRemoteSessionWithoutControllerEnvironment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	executable := commandTestExecutable(t, root, "production-browser-readiness")
	var captured []string
	deps := dependencies{
		getenv:     func(string) string { return "" },
		getwd:      func() (string, error) { return "", os.ErrNotExist },
		executable: func() (string, error) { return executable, nil },
		newClient: func(productionbrowserreadiness.GCloudConfig) (productionbrowserreadiness.TransportClient, error) {
			return nil, os.ErrInvalid
		},
		run: func(context.Context, productionbrowserreadiness.TransportClient, productionbrowserreadiness.Request, io.Writer, io.Writer) productionbrowserreadiness.Result {
			return productionbrowserreadiness.Result{ExitCode: 99}
		},
		remote: func(_ context.Context, args []string, gotExecutable string, _, _ io.Writer) int {
			captured = append([]string(nil), args...)
			if gotExecutable != executable {
				t.Fatalf("remote executable = %q", gotExecutable)
			}
			return 38
		},
	}
	status := run(context.Background(), []string{"remote-session", "cleanup", "1", "1", "/tmp/stage", "digest"}, nil, nil, deps)
	if status != 38 || len(captured) != 5 || captured[0] != "cleanup" {
		t.Fatalf("remote dispatch = status %d args %#v", status, captured)
	}
}

func TestRunWithSignalsDeliversTermCause(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	executable := commandTestExecutable(t, root, "production-browser-readiness")
	cloudReadiness := commandTestExecutable(t, root, "cloud-run-readiness")
	artifacts := filepath.Join(root, "artifacts")
	if err := os.Mkdir(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	environment := commandTestEnvironment(root, cloudReadiness)
	started := make(chan struct{})
	deps := dependencies{
		getenv:     func(name string) string { return environment[name] },
		getwd:      func() (string, error) { return root, nil },
		executable: func() (string, error) { return executable, nil },
		newClient: func(productionbrowserreadiness.GCloudConfig) (productionbrowserreadiness.TransportClient, error) {
			return nil, nil
		},
		run: func(ctx context.Context, _ productionbrowserreadiness.TransportClient, _ productionbrowserreadiness.Request, _, _ io.Writer) productionbrowserreadiness.Result {
			close(started)
			<-ctx.Done()
			return productionbrowserreadiness.Result{ExitCode: productionbrowserreadiness.ExitTerminated}
		},
		remote: func(context.Context, []string, string, io.Writer, io.Writer) int { return 99 },
	}
	signals := make(chan os.Signal, 1)
	done := make(chan int, 1)
	go func() {
		done <- runWithSignals([]string{
			"scribe-browser-acde1234",
			"scribe-browser-session-acde1234",
			filepath.Join(artifacts, "readiness.log"),
		}, nil, nil, signals, deps)
	}()
	<-started
	signals <- syscall.SIGTERM
	if status := <-done; status != productionbrowserreadiness.ExitTerminated {
		t.Fatalf("runWithSignals() status = %d", status)
	}
}

func commandTestEnvironment(root, cloudReadiness string) map[string]string {
	return map[string]string{
		"GCLOUD_PROJECT":                 "scribe-test",
		"SCRIBE_DEPLOYMENT_ENVIRONMENT":  "production",
		"SCRIBE_REGION":                  "us-east5",
		"SCRIBE_ZONE":                    "us-east5-b",
		"SCRIBE_INSTANCE":                "scribe",
		"GITHUB_RUN_ID":                  "76543210",
		"GITHUB_RUN_ATTEMPT":             "3",
		"RUNNER_TEMP":                    root,
		"SCRIBE_CLOUD_RUN_READINESS_BIN": cloudReadiness,
	}
}

func commandTestExecutable(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
