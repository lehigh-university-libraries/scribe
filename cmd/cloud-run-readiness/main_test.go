package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/cloudrunreadiness"
)

func TestRunPreservesRunnerResultAndStreams(t *testing.T) {
	t.Parallel()

	diagnosticsPath := filepath.Join(t.TempDir(), "backend.log")
	environment := validEnvironment()
	var receivedConfig cloudrunreadiness.GCloudConfig
	var receivedRequest cloudrunreadiness.Request
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{
		"scribe-prod-backend-readiness",
		"backend",
		diagnosticsPath,
	}, &stdout, &stderr, dependencies{
		getenv: mapEnvironment(environment),
		newClient: func(config cloudrunreadiness.GCloudConfig) (cloudrunreadiness.Client, error) {
			receivedConfig = config
			return nil, nil
		},
		execute: func(
			_ context.Context,
			_ cloudrunreadiness.Client,
			request cloudrunreadiness.Request,
			stdout io.Writer,
			stderr io.Writer,
		) cloudrunreadiness.Result {
			receivedRequest = request
			_, _ = io.WriteString(stdout, "runner stdout\n")
			_, _ = io.WriteString(stderr, "runner stderr\n")
			return cloudrunreadiness.Result{ExitCode: 37}
		},
	})

	if exitCode != 37 {
		t.Fatalf("exit code = %d, want 37", exitCode)
	}
	if got, want := stdout.String(), "runner stdout\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "runner stderr\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if receivedConfig.Project != environment["GCLOUD_PROJECT"] ||
		receivedConfig.Region != environment["SCRIBE_REGION"] ||
		receivedConfig.Executable != "gcloud" {
		t.Fatalf("gcloud config = %+v", receivedConfig)
	}
	if receivedRequest.Job != "scribe-prod-backend-readiness" ||
		receivedRequest.Kind != cloudrunreadiness.KindBackend ||
		receivedRequest.DiagnosticsPath != diagnosticsPath {
		t.Fatalf("request = %+v", receivedRequest)
	}
}

func TestRunPrintsOnlyBoundedRequestErrors(t *testing.T) {
	t.Parallel()

	const rejectedInput = "unbounded-secret-input"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	clientCalled := false
	executeCalled := false
	exitCode := run(context.Background(), []string{rejectedInput}, &stdout, &stderr, dependencies{
		getenv: mapEnvironment(validEnvironment()),
		newClient: func(cloudrunreadiness.GCloudConfig) (cloudrunreadiness.Client, error) {
			clientCalled = true
			return nil, nil
		},
		execute: func(context.Context, cloudrunreadiness.Client, cloudrunreadiness.Request, io.Writer, io.Writer) cloudrunreadiness.Result {
			executeCalled = true
			return cloudrunreadiness.Result{}
		},
	})

	if exitCode != configurationExitCode {
		t.Fatalf("exit code = %d, want %d", exitCode, configurationExitCode)
	}
	if clientCalled || executeCalled {
		t.Fatal("invalid request reached execution dependencies")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "arguments must be") {
		t.Fatalf("stderr = %q, want bounded argument rule", stderr.String())
	}
	if strings.Contains(stderr.String(), rejectedInput) {
		t.Fatalf("stderr disclosed rejected input: %q", stderr.String())
	}
}

func TestRunPrintsClientConfigurationErrorWithoutExecuting(t *testing.T) {
	t.Parallel()

	diagnosticsPath := filepath.Join(t.TempDir(), "backend.log")
	configurationError := &cloudrunreadiness.ValidationError{
		Field: "gcloud executable",
		Rule:  "must be a valid path",
	}
	var stderr bytes.Buffer
	executeCalled := false
	exitCode := run(context.Background(), []string{
		"scribe-prod-backend-readiness",
		"backend",
		diagnosticsPath,
	}, io.Discard, &stderr, dependencies{
		getenv: mapEnvironment(validEnvironment()),
		newClient: func(cloudrunreadiness.GCloudConfig) (cloudrunreadiness.Client, error) {
			return nil, configurationError
		},
		execute: func(context.Context, cloudrunreadiness.Client, cloudrunreadiness.Request, io.Writer, io.Writer) cloudrunreadiness.Result {
			executeCalled = true
			return cloudrunreadiness.Result{}
		},
	})

	if exitCode != configurationExitCode {
		t.Fatalf("exit code = %d, want %d", exitCode, configurationExitCode)
	}
	if executeCalled {
		t.Fatal("execution ran after client configuration failed")
	}
	if got, want := stderr.String(), "Cloud Run readiness helper failed: "+configurationError.Error()+"\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunRedactsUnexpectedClientErrors(t *testing.T) {
	t.Parallel()

	const sensitiveError = "gcloud failed with secret environment contents"
	diagnosticsPath := filepath.Join(t.TempDir(), "backend.log")
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{
		"scribe-prod-backend-readiness",
		"backend",
		diagnosticsPath,
	}, io.Discard, &stderr, dependencies{
		getenv: mapEnvironment(validEnvironment()),
		newClient: func(cloudrunreadiness.GCloudConfig) (cloudrunreadiness.Client, error) {
			return nil, errors.New(sensitiveError)
		},
		execute: func(context.Context, cloudrunreadiness.Client, cloudrunreadiness.Request, io.Writer, io.Writer) cloudrunreadiness.Result {
			t.Fatal("execution ran after client configuration failed")
			return cloudrunreadiness.Result{}
		},
	})

	if exitCode != configurationExitCode {
		t.Fatalf("exit code = %d, want %d", exitCode, configurationExitCode)
	}
	if strings.Contains(stderr.String(), sensitiveError) {
		t.Fatalf("stderr disclosed unexpected client error: %q", stderr.String())
	}
	if got, want := stderr.String(), "Cloud Run readiness helper failed: invalid readiness client configuration\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunWithSignalsPropagatesSignalExitCause(t *testing.T) {
	tests := []struct {
		name     string
		signal   os.Signal
		exitCode int
	}{
		{name: "interrupt", signal: syscall.SIGINT, exitCode: 130},
		{name: "terminate", signal: syscall.SIGTERM, exitCode: 143},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signals := make(chan os.Signal, 1)
			signals <- test.signal
			var receivedCause error
			exitCode := runWithSignals([]string{
				"--preflight-only",
				"scribe-prod-backend-readiness",
				"backend",
			}, io.Discard, io.Discard, signals, dependencies{
				getenv: mapEnvironment(validEnvironment()),
				newClient: func(cloudrunreadiness.GCloudConfig) (cloudrunreadiness.Client, error) {
					return nil, nil
				},
				execute: func(ctx context.Context, _ cloudrunreadiness.Client, _ cloudrunreadiness.Request, _, _ io.Writer) cloudrunreadiness.Result {
					<-ctx.Done()
					receivedCause = context.Cause(ctx)
					return cloudrunreadiness.Result{ExitCode: test.exitCode}
				},
			})

			if exitCode != test.exitCode {
				t.Fatalf("exit code = %d, want %d", exitCode, test.exitCode)
			}
			if !errors.Is(receivedCause, cloudrunreadiness.SignalCause(test.exitCode)) {
				t.Fatalf("cancellation cause = %v, want signal exit %d", receivedCause, test.exitCode)
			}
		})
	}
}

func TestRunRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	if got, want := run(context.Background(), nil, io.Discard, &stderr, dependencies{}), configurationExitCode; got != want {
		t.Fatalf("exit code = %d, want %d", got, want)
	}
	if got, want := stderr.String(), "Cloud Run readiness helper failed: command dependencies are not configured\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		"GCLOUD_PROJECT": "scribe-prod-123",
		"SCRIBE_REGION":  "us-east5",
	}
}

func mapEnvironment(values map[string]string) func(string) string {
	return func(name string) string {
		return values[name]
	}
}
