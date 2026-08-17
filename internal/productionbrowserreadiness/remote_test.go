package productionbrowserreadiness

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRemotePrepareReservesAfterTypedCleanup(t *testing.T) {
	stage := t.TempDir()
	runner := &remoteCommandRunner{}
	session := remoteSession{request: remoteTestRequest(t, stage), run: runner}
	if err := session.prepare(context.Background()); err != nil {
		t.Fatalf("prepare() error = %v", err)
	}
	operations := runner.browserSessionOperations()
	want := []string{"--cleanup", "--cleanup-all", "--reserve"}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("browser-session operations = %#v, want %#v", operations, want)
	}
	assertManagedDockerConfig(t, runner.environments)
}

func TestRemoteMintConsumesReservationAndDeletesContainerStateImmediately(t *testing.T) {
	stage := t.TempDir()
	request := remoteTestRequest(t, stage)
	runner := &remoteCommandRunner{statePath: request.statePath}
	session := remoteSession{request: request, run: runner}
	if stage := session.mint(context.Background()); stage != remoteMintSucceeded {
		t.Fatalf("mint() stage = %v", stage)
	}
	operations := runner.browserSessionOperations()
	want := []string{"--reserved-output", "--export", "--cleanup"}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("browser-session operations = %#v, want %#v", operations, want)
	}
	exportIndex := runner.indexContaining("--export /tmp/scribe-browser-session-")
	cleanupIndex := runner.indexContaining("--cleanup /tmp/scribe-browser-session-")
	if exportIndex < 0 || cleanupIndex <= exportIndex || runner.indexContaining(" cp ") >= 0 {
		t.Fatalf("container export/cleanup order = %#v", runner.arguments)
	}
	info, err := os.Lstat(runner.statePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("host state = %v, %v", info, err)
	}
	assertManagedDockerConfig(t, runner.environments)
}

func TestRemoteStateExportRefusesExistingSymlinkWithoutTouchingTarget(t *testing.T) {
	stage := t.TempDir()
	request := remoteTestRequest(t, stage)
	target := filepath.Join(t.TempDir(), "operator-file")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, request.statePath); err != nil {
		t.Fatal(err)
	}
	session := remoteSession{request: request, run: &remoteCommandRunner{}}
	if err := session.exportState(context.Background()); err == nil {
		t.Fatal("state export followed an existing symlink")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "unchanged" {
		t.Fatalf("symlink target = %q, %v", contents, err)
	}
}

func TestRemoteMintDropsHostStateWhenPostMintCleanupFails(t *testing.T) {
	stage := t.TempDir()
	request := remoteTestRequest(t, stage)
	runner := &remoteCommandRunner{
		statePath:       request.statePath,
		failCleanupCall: 2,
	}
	session := remoteSession{request: request, run: runner}
	if status := session.execute(context.Background()); status != remoteMintRecoveryCleanupExitCode {
		t.Fatalf("remote mint status = %d, want recovery cleanup status %d", status, remoteMintRecoveryCleanupExitCode)
	}
	if _, err := os.Lstat(runner.statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ineligible host state remained: %v", err)
	}
	if runner.cleanupCalls < 3 {
		t.Fatalf("cleanup calls = %d, want immediate, post-mint, and reconciliation", runner.cleanupCalls)
	}
}

func TestRemoteMintEmitsOnlyFixedSubstageStatusesAndStillCleans(t *testing.T) {
	tests := []struct {
		name          string
		results       []commandResult
		stateContents []byte
		wantStatus    int
		wantCleanups  int
	}{
		{
			name: "session command",
			results: []commandResult{
				{stdout: []byte("session-cookie=super-secret"), exitCode: 37, err: errCommandFailed},
				{},
			},
			wantStatus:   remoteMintSessionCommandExitCode,
			wantCleanups: 1,
		},
		{
			name: "state export",
			results: []commandResult{
				{stdout: []byte("session-cookie=super-secret")},
				{stdout: []byte("storage-state=super-secret"), exitCode: 38, err: errCommandFailed},
				{},
			},
			wantStatus:   remoteMintStateExportExitCode,
			wantCleanups: 1,
		},
		{
			name: "oversized state export",
			results: []commandResult{
				{},
				{},
				{},
			},
			stateContents: []byte(strings.Repeat("x", maximumStateBytes+2)),
			wantStatus:    remoteMintStateExportExitCode,
			wantCleanups:  1,
		},
		{
			name: "container cleanup",
			results: []commandResult{
				{stdout: []byte("session-cookie=super-secret")},
				{stdout: []byte("storage-state=super-secret")},
				{stdout: []byte("cleanup-secret=super-secret"), exitCode: 39, err: errCommandFailed},
				{},
			},
			wantStatus:   remoteMintContainerCleanupExitCode,
			wantCleanups: 2,
		},
		{
			name: "host state contract",
			results: []commandResult{
				{stdout: []byte("session-cookie=super-secret")},
				{stdout: []byte("storage-state=super-secret")},
				{},
				{},
			},
			stateContents: []byte("too-short-secret-state"),
			wantStatus:    remoteMintHostStateContractExitCode,
			wantCleanups:  2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stage := t.TempDir()
			request := remoteTestRequest(t, stage)
			commandRunner := &remoteCommandRunner{
				results:       append([]commandResult(nil), test.results...),
				statePath:     request.statePath,
				stateContents: test.stateContents,
			}
			session := remoteSession{request: request, run: commandRunner}
			status := session.execute(context.Background())
			if status != test.wantStatus {
				t.Fatalf("execute() status = %d, want %d; commands = %#v", status, test.wantStatus, commandRunner.arguments)
			}
			if commandRunner.cleanupCalls != test.wantCleanups {
				t.Fatalf("cleanup calls = %d, want %d; commands = %#v", commandRunner.cleanupCalls, test.wantCleanups, commandRunner.arguments)
			}
			if cleanupIndex := commandRunner.lastIndexContaining("--cleanup /tmp/scribe-browser-session-"); cleanupIndex != len(commandRunner.arguments)-1 {
				t.Fatalf("recovery cleanup was not last: %#v", commandRunner.arguments)
			}
			if _, err := os.Lstat(request.statePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed mint retained host state: %v", err)
			}

			var stderr bytes.Buffer
			writeRemoteSessionStatus(&stderr, status)
			if got := stderr.String(); got != "Production browser remote session failed: operation.\n" || strings.Contains(got, "super-secret") {
				t.Fatalf("remote stderr was not fixed and redacted: %q", got)
			}
		})
	}
}

func TestRemoteCleanupKeepsExecutableRetryableUntilControllerAcknowledges(t *testing.T) {
	stage := t.TempDir()
	request := remoteTestRequest(t, stage)
	if err := os.WriteFile(request.statePath, []byte(strings.Repeat("x", minimumStateBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.executablePath, []byte("transport"), 0o700); err != nil {
		t.Fatal(err)
	}
	session := remoteSession{request: request, run: &remoteCommandRunner{}}
	if err := session.cleanupAll(context.Background()); err != nil {
		t.Fatalf("cleanupAll() error = %v", err)
	}
	if _, err := os.Lstat(request.stateDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state directory remained after typed cleanup: %v", err)
	}
	if info, err := os.Lstat(request.executablePath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("retryable executable was removed before controller acknowledgement: %v, %v", info, err)
	}
}

func TestRemoteStageLockSerializesAndFinalizes(t *testing.T) {
	stage := t.TempDir()
	first, err := acquireRemoteLock(context.Background(), stage)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = acquireRemoteLock(ctx, stage)
	cancel()
	if err == nil {
		t.Fatal("second stage lock ignored the held lock")
	}
	if err := first.finalize(stage); err != nil {
		t.Fatalf("finalize lock: %v", err)
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage remained after finalization: %v", err)
	}
}

func TestSweepStaleStagesRefusesHeldStageLock(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, ".scribe-production-browser-987654321012345678-1.AbCdEf1234")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := acquireRemoteLock(context.Background(), stage)
	if err != nil {
		t.Fatal(err)
	}
	currentStage := filepath.Join(root, ".scribe-production-browser-76543210-3.ZyXwVu9876")
	if err := os.Mkdir(currentStage, 0o700); err != nil {
		t.Fatal(err)
	}
	session := remoteSession{request: remoteTestRequest(t, currentStage), run: &remoteCommandRunner{}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = session.sweepStaleStages(ctx)
	cancel()
	if err == nil {
		t.Fatal("stale sweep ignored a live stage lock")
	}
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("held stale stage was removed: %v", err)
	}
	if err := held.release(); err != nil {
		t.Fatal(err)
	}
	if err := session.sweepStaleStages(context.Background()); err != nil {
		t.Fatalf("stale sweep after release: %v", err)
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released stale stage remained: %v", err)
	}
}

func TestSweepLegacyNonExecutableStageAndOrphanStateDirectory(t *testing.T) {
	legacyStage := filepath.Join("/tmp", "scribe-production-browser-987654321012345679-1.QwErTy1234")
	_ = os.RemoveAll(legacyStage)
	if err := os.Mkdir(legacyStage, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(legacyStage) })
	if err := os.WriteFile(filepath.Join(legacyStage, remoteBinaryName), []byte("old transport"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyStage, "storage-state.json"), []byte("old state"), 0o600); err != nil {
		t.Fatal(err)
	}

	orphanState := remoteStateDirectory("987654321012345680", "1", "AsDfGh5678")
	_ = os.RemoveAll(orphanState)
	if err := os.Mkdir(orphanState, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(orphanState) })
	if err := os.WriteFile(filepath.Join(orphanState, "storage-state.json"), []byte("orphan state"), 0o600); err != nil {
		t.Fatal(err)
	}

	currentStage := t.TempDir()
	session := remoteSession{request: remoteTestRequest(t, currentStage), run: &remoteCommandRunner{}}
	if err := session.sweepLegacyStages(context.Background()); err != nil {
		t.Fatalf("legacy stage sweep: %v", err)
	}
	if err := session.sweepStaleStateDirectories(); err != nil {
		t.Fatalf("orphan state sweep: %v", err)
	}
	for _, path := range []string{legacyStage, orphanState} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale path remained %s: %v", path, err)
		}
	}
}

func TestRemoteInvocationIsDigestAttestedAndScoped(t *testing.T) {
	request := Request{RunID: "76543210", RunAttempt: "3"}
	stage := remoteStagePrefix(request.RunID, request.RunAttempt) + "AbCdEf1234"
	digest := strings.Repeat("a", 64)
	command := remoteInvocationCommand(filepath.Join(stage, remoteBinaryName), remoteMint, request, stage, digest)
	for _, required := range []string{
		"chmod 0700",
		"sha256sum",
		"test \"$actual\" = '" + digest + "'",
		"remote-session 'mint' '76543210' '3'",
	} {
		if !strings.Contains(command, required) {
			t.Fatalf("remote command missing %q: %s", required, command)
		}
	}
	if strings.Contains(command, "status=0") || strings.Contains(command, "trap ") || strings.Contains(command, "while ") {
		t.Fatalf("remote bootstrap contains lifecycle state: %s", command)
	}
	if strings.HasPrefix(filepath.Join(stage, remoteBinaryName), "/tmp/") {
		t.Fatalf("remote executable remained on the non-executable COS /tmp mount: %s", stage)
	}
}

func TestRemoteStageAndStatePathsHaveSeparateFilesystemRoles(t *testing.T) {
	t.Parallel()
	request := Request{RunID: "76543210", RunAttempt: "3"}
	stage := remoteStagePrefix(request.RunID, request.RunAttempt) + "AbCdEf1234"
	stateDirectory, valid := remoteStateDirectoryForStage(request, stage)
	if !valid {
		t.Fatal("valid remote stage was rejected")
	}
	if filepath.Dir(stage) != remoteProjectDirectory || !strings.HasPrefix(filepath.Base(stage), remoteStageNamePrefix) {
		t.Fatalf("executable stage = %q", stage)
	}
	if filepath.Dir(stateDirectory) != "/tmp" || filepath.Base(stateDirectory) != "scribe-production-browser-state-76543210-3.AbCdEf1234" {
		t.Fatalf("ephemeral state directory = %q", stateDirectory)
	}
	if _, valid := remoteStateDirectoryForStage(request, "/tmp/scribe-production-browser-76543210-3.AbCdEf1234"); valid {
		t.Fatal("legacy non-executable stage was accepted")
	}
}

func TestRemoteStateDirectoryCleanupIsRetryIdempotent(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "storage-state.json"), []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeRemoteStateDirectory(directory); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if err := removeRemoteStateDirectory(directory); err != nil {
		t.Fatalf("absent cleanup retry: %v", err)
	}
}

func remoteTestRequest(t *testing.T, stage string) remoteRequest {
	t.Helper()
	stateDirectory := stage + ".state"
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDirectory) })
	return remoteRequest{
		mode:               remoteMint,
		runID:              "76543210",
		runAttempt:         "3",
		stageDirectory:     stage,
		stateDirectory:     stateDirectory,
		statePath:          filepath.Join(stateDirectory, "storage-state.json"),
		containerStatePath: "/tmp/scribe-browser-session-76543210-3.json",
		executablePath:     filepath.Join(stage, remoteBinaryName),
		dockerExecutable:   "/tools/docker",
	}
}

type remoteCommandRunner struct {
	arguments       [][]string
	environments    []map[string]string
	results         []commandResult
	statePath       string
	stateContents   []byte
	cleanupCalls    int
	failCleanupCall int
}

func (runner *remoteCommandRunner) Run(
	_ context.Context,
	_ string,
	args []string,
	environment map[string]string,
	_, _ time.Duration,
	_ int,
) commandResult {
	runner.arguments = append(runner.arguments, append([]string(nil), args...))
	runner.environments = append(runner.environments, cloneStrings(environment))
	if !reflect.DeepEqual(environment, map[string]string{"DOCKER_CONFIG": remoteDockerConfig}) {
		return commandResult{exitCode: 125, err: errCommandFailed}
	}
	result := commandResult{}
	if len(runner.results) > 0 {
		result = runner.results[0]
		runner.results = runner.results[1:]
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, " /app/scribe-browser-session --cleanup ") {
		runner.cleanupCalls++
		if runner.cleanupCalls == runner.failCleanupCall {
			return commandResult{exitCode: 31, err: errCommandFailed}
		}
	}
	if result.err != nil || result.exitCode != 0 {
		return result
	}
	return result
}

func (runner *remoteCommandRunner) RunStream(
	_ context.Context,
	_ string,
	args []string,
	environment map[string]string,
	_, _ time.Duration,
	destination io.Writer,
	maximumBytes int,
) commandResult {
	runner.arguments = append(runner.arguments, append([]string(nil), args...))
	runner.environments = append(runner.environments, cloneStrings(environment))
	if !reflect.DeepEqual(environment, map[string]string{"DOCKER_CONFIG": remoteDockerConfig}) ||
		!strings.Contains(strings.Join(args, " "), " /app/scribe-browser-session --export /tmp/scribe-browser-session-") {
		return commandResult{exitCode: 125, err: errCommandFailed}
	}
	result := commandResult{}
	if len(runner.results) > 0 {
		result = runner.results[0]
		runner.results = runner.results[1:]
	}
	contents := runner.stateContents
	if contents == nil {
		contents = []byte(strings.Repeat("x", minimumStateBytes))
	}
	if result.err != nil || result.exitCode != 0 {
		contents = result.stdout
	}
	stream := newCappedStreamWriter(destination, maximumBytes)
	if _, err := stream.Write(contents); err != nil {
		return commandResult{exitCode: 125, err: errCommandFailed}
	}
	if stream.exceeded {
		return commandResult{exitCode: 125, err: errOutputLimit}
	}
	return commandResult{exitCode: result.exitCode, err: result.err}
}

func assertManagedDockerConfig(t *testing.T, environments []map[string]string) {
	t.Helper()
	if len(environments) == 0 {
		t.Fatal("no Docker command environments recorded")
	}
	want := map[string]string{"DOCKER_CONFIG": remoteDockerConfig}
	for index, environment := range environments {
		if !reflect.DeepEqual(environment, want) {
			t.Fatalf("Docker command %d environment = %#v, want %#v", index, environment, want)
		}
	}
}

func (runner *remoteCommandRunner) browserSessionOperations() []string {
	operations := make([]string, 0)
	for _, args := range runner.arguments {
		for index, argument := range args {
			if argument != "/app/scribe-browser-session" || index+1 >= len(args) {
				continue
			}
			operations = append(operations, args[index+1])
		}
	}
	return operations
}

func (runner *remoteCommandRunner) indexContaining(fragment string) int {
	for index, args := range runner.arguments {
		if strings.Contains(strings.Join(args, " "), fragment) {
			return index
		}
	}
	return -1
}

func (runner *remoteCommandRunner) lastIndexContaining(fragment string) int {
	for index := len(runner.arguments) - 1; index >= 0; index-- {
		if strings.Contains(strings.Join(runner.arguments[index], " "), fragment) {
			return index
		}
	}
	return -1
}
