package productionbrowserreadiness

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const maximumCommandOutput = 8 << 20

var (
	errCommandFailed = errors.New("production browser command failed")
	errOutputLimit   = errors.New("production browser command output exceeded its limit")
)

type commandResult struct {
	stdout   []byte
	exitCode int
	err      error
}

type commandRunner interface {
	Run(context.Context, string, []string, map[string]string, time.Duration, time.Duration, int) commandResult
}

type streamCommandRunner interface {
	RunStream(context.Context, string, []string, map[string]string, time.Duration, time.Duration, io.Writer, int) commandResult
}

type osCommandRunner struct{}

func (osCommandRunner) Run(
	ctx context.Context,
	executable string,
	args []string,
	environment map[string]string,
	timeout time.Duration,
	killGrace time.Duration,
	outputLimit int,
) commandResult {
	buffer := newBoundedBuffer(outputLimit)
	result := executeCommand(ctx, executable, args, environment, timeout, killGrace, buffer)
	if buffer.exceeded {
		return commandResult{stdout: buffer.Bytes(), exitCode: 125, err: errOutputLimit}
	}
	result.stdout = buffer.Bytes()
	return result
}

// RunStream connects child stdout to destination through a bounded writer.
// It never retains or returns the streamed bytes in commandResult.
func (osCommandRunner) RunStream(
	ctx context.Context,
	executable string,
	args []string,
	environment map[string]string,
	timeout time.Duration,
	killGrace time.Duration,
	destination io.Writer,
	maximumBytes int,
) commandResult {
	if destination == nil || maximumBytes <= 0 {
		return commandResult{exitCode: 125, err: errCommandFailed}
	}
	stream := newCappedStreamWriter(destination, maximumBytes)
	result := executeCommand(ctx, executable, args, environment, timeout, killGrace, stream)
	if stream.exceeded {
		return commandResult{exitCode: 125, err: errOutputLimit}
	}
	return result
}

func executeCommand(
	ctx context.Context,
	executable string,
	args []string,
	environment map[string]string,
	timeout time.Duration,
	killGrace time.Duration,
	stdout io.Writer,
) commandResult {
	if ctx == nil {
		ctx = context.Background()
	}
	callContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := callContext.Err(); err != nil {
		return commandResult{exitCode: 125, err: errCommandFailed}
	}

	// #nosec G204 -- executable is resolved once from fixed command
	// configuration and every argv value is validated before direct execution.
	command := exec.Command(executable, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	command.Env = mergeEnvironment(os.Environ(), environment)
	command.Stdout = stdout
	command.Stderr = io.Discard
	command.Stdin = nil
	if err := command.Start(); err != nil {
		return commandResult{exitCode: 125, err: errCommandFailed}
	}

	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	waitError := error(nil)
	select {
	case waitError = <-done:
	case <-callContext.Done():
		_ = signalProcessGroup(command.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(killGrace)
		select {
		case waitError = <-done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = signalProcessGroup(command.Process.Pid, syscall.SIGKILL)
			waitError = <-done
		}
		if waitError == nil {
			waitError = errCommandFailed
		}
	}

	if waitError == nil {
		return commandResult{exitCode: 0}
	}
	exitCode := 125
	var exitError *exec.ExitError
	if errors.As(waitError, &exitError) {
		exitCode = exitError.ExitCode()
		if exitCode <= 0 || exitCode > 255 {
			exitCode = 125
		}
	}
	return commandResult{exitCode: exitCode, err: errCommandFailed}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return errCommandFailed
	}
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	filtered := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replaced := overrides[name]; replaced {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	for name, value := range overrides {
		filtered = append(filtered, name+"="+value)
	}
	return filtered
}

func resolveExecutable(candidate string) (string, error) {
	if candidate == "" || strings.ContainsAny(candidate, "\r\n\x00") {
		return "", errCommandFailed
	}
	resolved, err := exec.LookPath(candidate)
	if err != nil {
		return "", errCommandFailed
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", errCommandFailed
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", errCommandFailed
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errCommandFailed
	}
	return resolved, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

// cappedStreamWriter writes at most maximumBytes plus one sentinel byte. The
// sentinel proves overflow without retaining streamed credential material.
type cappedStreamWriter struct {
	destination io.Writer
	maximum     int
	written     int
	exceeded    bool
}

func newCappedStreamWriter(destination io.Writer, maximum int) *cappedStreamWriter {
	return &cappedStreamWriter{destination: destination, maximum: maximum}
}

func (writer *cappedStreamWriter) Write(data []byte) (int, error) {
	requested := len(data)
	remaining := writer.maximum + 1 - writer.written
	if remaining < 0 {
		remaining = 0
	}
	if len(data) > remaining {
		data = data[:remaining]
	}
	if len(data) > 0 {
		written, err := writer.destination.Write(data)
		writer.written += written
		if err != nil {
			return written, err
		}
		if written != len(data) {
			return written, io.ErrShortWrite
		}
	}
	if writer.written > writer.maximum || requested > remaining {
		writer.exceeded = true
	}
	return requested, nil
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit - buffer.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = buffer.Buffer.Write(data[:remaining])
	}
	if remaining < len(data) {
		buffer.exceeded = true
	}
	return written, nil
}
