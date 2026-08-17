// Command production-browser-readiness runs the typed production browser
// credential transport and its VM-side remote-session boundary.
package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/lehigh-university-libraries/scribe/internal/productionbrowserreadiness"
)

type dependencies struct {
	getenv     func(string) string
	getwd      func() (string, error)
	executable func() (string, error)
	newClient  func(productionbrowserreadiness.GCloudConfig) (productionbrowserreadiness.TransportClient, error)
	run        func(context.Context, productionbrowserreadiness.TransportClient, productionbrowserreadiness.Request, io.Writer, io.Writer) productionbrowserreadiness.Result
	remote     func(context.Context, []string, string, io.Writer, io.Writer) int
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
		getenv:     os.Getenv,
		getwd:      os.Getwd,
		executable: os.Executable,
		newClient: func(config productionbrowserreadiness.GCloudConfig) (productionbrowserreadiness.TransportClient, error) {
			return productionbrowserreadiness.NewGCloudClient(config)
		},
		run: func(
			ctx context.Context,
			client productionbrowserreadiness.TransportClient,
			request productionbrowserreadiness.Request,
			stdout, stderr io.Writer,
		) productionbrowserreadiness.Result {
			return productionbrowserreadiness.NewRunner(client).Run(ctx, request, stdout, stderr)
		},
		remote: productionbrowserreadiness.RunRemoteSession,
	}
}

func runWithSignals(args []string, stdout, stderr io.Writer, signals <-chan os.Signal, deps dependencies) int {
	ctx, cancel := context.WithCancelCause(context.Background())
	finished := make(chan struct{})
	go func() {
		select {
		case processSignal := <-signals:
			switch processSignal {
			case syscall.SIGTERM:
				cancel(productionbrowserreadiness.SignalCause(productionbrowserreadiness.ExitTerminated))
			default:
				cancel(productionbrowserreadiness.SignalCause(productionbrowserreadiness.ExitInterrupted))
			}
		case <-finished:
		}
	}()
	status := run(ctx, args, stdout, stderr, deps)
	close(finished)
	cancel(nil)
	return status
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) int {
	if deps.getenv == nil || deps.getwd == nil || deps.executable == nil || deps.newClient == nil || deps.run == nil || deps.remote == nil {
		_, _ = io.WriteString(stderr, "Production browser readiness command failed: invalid configuration.\n")
		return productionbrowserreadiness.ExitInvalidInvocation
	}
	executable, err := deps.executable()
	if err != nil {
		_, _ = io.WriteString(stderr, "Production browser readiness command failed: executable unavailable.\n")
		return productionbrowserreadiness.ExitInvalidInvocation
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		_, _ = io.WriteString(stderr, "Production browser readiness command failed: executable unavailable.\n")
		return productionbrowserreadiness.ExitInvalidInvocation
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		_, _ = io.WriteString(stderr, "Production browser readiness command failed: executable unavailable.\n")
		return productionbrowserreadiness.ExitInvalidInvocation
	}
	if len(args) > 0 && args[0] == "remote-session" {
		return deps.remote(ctx, args[1:], executable, stdout, stderr)
	}
	workingDirectory, err := deps.getwd()
	if err != nil {
		_, _ = io.WriteString(stderr, "Production browser readiness command failed: working directory unavailable.\n")
		return productionbrowserreadiness.ExitInvalidInvocation
	}
	request, err := productionbrowserreadiness.ParseRequest(args, deps.getenv, workingDirectory, executable)
	if err != nil {
		if !productionbrowserreadiness.IsValidationError(err) {
			err = errors.New("invalid request")
		}
		_, _ = io.WriteString(stderr, "Production browser readiness command failed: "+err.Error()+".\n")
		return productionbrowserreadiness.ExitInvalidInvocation
	}
	client, err := deps.newClient(productionbrowserreadiness.GCloudConfig{})
	if err != nil {
		_, _ = io.WriteString(stderr, "Production browser readiness command failed: client unavailable.\n")
		return productionbrowserreadiness.ExitInvalidInvocation
	}
	return deps.run(ctx, client, request, stdout, stderr).ExitCode
}
