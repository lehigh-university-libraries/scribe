// Command cloud-run-readiness runs the typed Cloud Run readiness lifecycle.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lehigh-university-libraries/scribe/internal/cloudrunreadiness"
)

const configurationExitCode = cloudrunreadiness.ExitInvalidInvocation

type dependencies struct {
	getenv    func(string) string
	newClient func(cloudrunreadiness.GCloudConfig) (cloudrunreadiness.Client, error)
	execute   func(context.Context, cloudrunreadiness.Client, cloudrunreadiness.Request, io.Writer, io.Writer) cloudrunreadiness.Result
}

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr, productionDependencies()))
}

func realMain(args []string, stdout, stderr io.Writer, deps dependencies) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	return runWithSignals(args, stdout, stderr, signals, deps)
}

func productionDependencies() dependencies {
	return dependencies{
		getenv: os.Getenv,
		newClient: func(config cloudrunreadiness.GCloudConfig) (cloudrunreadiness.Client, error) {
			return cloudrunreadiness.NewGCloudClient(config)
		},
		execute: func(
			ctx context.Context,
			client cloudrunreadiness.Client,
			request cloudrunreadiness.Request,
			stdout io.Writer,
			stderr io.Writer,
		) cloudrunreadiness.Result {
			return cloudrunreadiness.NewRunner(client).Run(ctx, request, stdout, stderr)
		},
	}
}

func runWithSignals(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	signals <-chan os.Signal,
	deps dependencies,
) int {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case processSignal := <-signals:
			switch processSignal {
			case syscall.SIGINT:
				cancel(cloudrunreadiness.SignalCause(cloudrunreadiness.ExitInterrupted))
			case syscall.SIGTERM:
				cancel(cloudrunreadiness.SignalCause(cloudrunreadiness.ExitTerminated))
			}
		case <-finished:
		}
	}()

	return run(ctx, args, stdout, stderr, deps)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) int {
	if deps.getenv == nil || deps.newClient == nil || deps.execute == nil {
		writeConfigurationError(stderr, errors.New("command dependencies are not configured"))
		return configurationExitCode
	}

	request, err := cloudrunreadiness.ParseRequest(args, deps.getenv)
	if err != nil {
		if !cloudrunreadiness.IsValidationError(err) {
			err = errors.New("invalid readiness request")
		}
		writeConfigurationError(stderr, err)
		return configurationExitCode
	}

	client, err := deps.newClient(cloudrunreadiness.GCloudConfig{
		Project:    request.Project,
		Region:     request.Region,
		Executable: "gcloud",
	})
	if err != nil {
		if !cloudrunreadiness.IsValidationError(err) {
			err = errors.New("invalid readiness client configuration")
		}
		writeConfigurationError(stderr, err)
		return configurationExitCode
	}

	result := deps.execute(ctx, client, request, stdout, stderr)
	return result.ExitCode
}

func writeConfigurationError(stderr io.Writer, err error) {
	_, _ = fmt.Fprintf(stderr, "Cloud Run readiness helper failed: %v\n", err)
}
