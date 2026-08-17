package productionbrowserreadiness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	remoteProjectDirectory = "/mnt/disks/data/scribe/prod"
	remoteComposeOverlay   = "/home/cloud-compose/scribe-runtime.compose.yaml"
	remoteComposeProject   = "scribe-prod"
	remoteDockerConfig     = "/mnt/disks/data/docker-config"
	remoteBinaryName       = "production-browser-readiness"
	remoteStageNamePrefix  = ".scribe-production-browser-"
	remoteStateNamePrefix  = "scribe-production-browser-state-"
	minimumStateBytes      = 128
	maximumStateBytes      = 8192
	maximumTransportBytes  = 64 << 20
	maximumStaleMaterials  = 64
	remoteCommandTimeout   = 150 * time.Second
	remoteCleanupTimeout   = 180 * time.Second
	remoteLockPoll         = 100 * time.Millisecond
)

var (
	remoteDigestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	remoteSuffixPattern   = regexp.MustCompile(`^[A-Za-z0-9]{10}$`)
	staleStagePattern     = regexp.MustCompile(`^\.scribe-production-browser-([1-9][0-9]{0,19})-([1-9][0-9]{0,4})\.([A-Za-z0-9]{10})$`)
	legacyStagePattern    = regexp.MustCompile(`^scribe-production-browser-[1-9][0-9]{0,19}-[1-9][0-9]{0,4}\.[A-Za-z0-9]{10}$`)
	remoteStatePattern    = regexp.MustCompile(`^scribe-production-browser-state-[1-9][0-9]{0,19}-[1-9][0-9]{0,4}\.[A-Za-z0-9]{10}$`)
	legacyMaterialPattern = regexp.MustCompile(`^scribe-production-browser-(helper-[1-9][0-9]{0,19}-[1-9][0-9]{0,4}\.sh|state-[1-9][0-9]{0,19}-[1-9][0-9]{0,4}\.json)$`)
)

type remoteMode string

const (
	remotePrepare remoteMode = "prepare"
	remoteMint    remoteMode = "mint"
	remoteCleanup remoteMode = "cleanup"
)

type remoteRequest struct {
	mode               remoteMode
	runID              string
	runAttempt         string
	stageDirectory     string
	stateDirectory     string
	statePath          string
	containerStatePath string
	executablePath     string
	dockerExecutable   string
}

type remoteSession struct {
	request remoteRequest
	run     interface {
		commandRunner
		streamCommandRunner
	}
}

// RunRemoteSession executes the narrow VM-side production session boundary.
// It emits only categorical errors and never prints credential material.
func RunRemoteSession(ctx context.Context, args []string, executablePath string, _ io.Writer, stderr io.Writer) int {
	if stderr == nil {
		stderr = io.Discard
	}
	request, err := parseRemoteRequest(args, executablePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "Production browser remote session failed: invalid request.")
		return ExitInvalidInvocation
	}
	lockContext := ctx
	lockCancel := func() {}
	if request.mode == remoteCleanup {
		lockContext, lockCancel = context.WithTimeout(context.Background(), remoteCleanupTimeout)
	}
	defer lockCancel()
	globalLock, err := acquireFileLock(lockContext, "/tmp/scribe-production-browser-controller.lock")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "Production browser remote session failed: serialization.")
		return operationExitCode(ctx)
	}
	lock, err := acquireRemoteLock(lockContext, request.stageDirectory)
	if err != nil {
		_ = globalLock.release()
		_, _ = fmt.Fprintln(stderr, "Production browser remote session failed: serialization.")
		return operationExitCode(ctx)
	}
	// Revalidate every owner, mode, digest, and project boundary after acquiring
	// the stage lock. A queued stale invocation cannot run after cleanup removes
	// the stage.
	request, err = parseRemoteRequest(args, executablePath)
	if err != nil {
		_ = lock.release()
		_ = globalLock.release()
		_, _ = fmt.Fprintln(stderr, "Production browser remote session failed: invalid request.")
		return ExitInvalidInvocation
	}
	session := remoteSession{request: request, run: osCommandRunner{}}
	status := session.execute(ctx)
	// Cleanup deliberately leaves the executable stage intact until its success
	// reaches the controller. The controller then removes only the exact inert
	// transport residue, so an ambiguous SSH exit can retry typed cleanup.
	if err := lock.release(); err != nil {
		status = ExitInvalidInvocation
	}
	if err := globalLock.release(); err != nil {
		status = ExitInvalidInvocation
	}
	writeRemoteSessionStatus(stderr, status)
	return status
}

func writeRemoteSessionStatus(stderr io.Writer, status int) {
	if status != ExitSuccess {
		_, _ = fmt.Fprintln(stderr, "Production browser remote session failed: operation.")
	}
}

func parseRemoteRequest(args []string, executablePath string) (remoteRequest, error) {
	if len(args) != 5 {
		return remoteRequest{}, errCommandFailed
	}
	mode := remoteMode(args[0])
	if mode != remotePrepare && mode != remoteMint && mode != remoteCleanup {
		return remoteRequest{}, errCommandFailed
	}
	if !runIDPattern.MatchString(args[1]) || !attemptPattern.MatchString(args[2]) || !remoteDigestPattern.MatchString(args[4]) {
		return remoteRequest{}, errCommandFailed
	}
	expectedStage := remoteStagePrefix(args[1], args[2])
	if !strings.HasPrefix(args[3], expectedStage) || len(args[3]) != len(expectedStage)+10 {
		return remoteRequest{}, errCommandFailed
	}
	suffix := strings.TrimPrefix(args[3], expectedStage)
	if !remoteSuffixPattern.MatchString(suffix) {
		return remoteRequest{}, errCommandFailed
	}
	stageDirectory := filepath.Clean(args[3])
	if stageDirectory != args[3] {
		return remoteRequest{}, errCommandFailed
	}
	if err := validateOwnedPrivateDirectory(stageDirectory, false); err != nil {
		return remoteRequest{}, errCommandFailed
	}
	projectRealPath, err := filepath.EvalSymlinks(remoteProjectDirectory)
	if err != nil || projectRealPath != remoteProjectDirectory {
		return remoteRequest{}, errCommandFailed
	}
	projectInfo, err := os.Lstat(remoteProjectDirectory)
	if err != nil || !projectInfo.IsDir() || projectInfo.Mode()&os.ModeSymlink != 0 {
		return remoteRequest{}, errCommandFailed
	}
	stateDirectory := remoteStateDirectory(args[1], args[2], suffix)
	if err := validateOwnedPrivateDirectory(stateDirectory, mode == remoteCleanup); err != nil {
		return remoteRequest{}, errCommandFailed
	}
	resolvedExecutable, err := filepath.EvalSymlinks(executablePath)
	expectedExecutable := filepath.Join(stageDirectory, remoteBinaryName)
	if err != nil || resolvedExecutable != expectedExecutable {
		return remoteRequest{}, errCommandFailed
	}
	info, err := os.Lstat(expectedExecutable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || fileOwner(info) != os.Geteuid() {
		return remoteRequest{}, errCommandFailed
	}
	digest, err := fileSHA256(expectedExecutable, maximumTransportBytes)
	if err != nil || digest != args[4] {
		return remoteRequest{}, errCommandFailed
	}
	dockerExecutable, err := resolveExecutable("docker")
	if err != nil {
		return remoteRequest{}, errCommandFailed
	}
	return remoteRequest{
		mode:               mode,
		runID:              args[1],
		runAttempt:         args[2],
		stageDirectory:     stageDirectory,
		stateDirectory:     stateDirectory,
		statePath:          filepath.Join(stateDirectory, "storage-state.json"),
		containerStatePath: fmt.Sprintf("/tmp/scribe-browser-session-%s-%s.json", args[1], args[2]),
		executablePath:     expectedExecutable,
		dockerExecutable:   dockerExecutable,
	}, nil
}

func remoteStagePrefix(runID, runAttempt string) string {
	return filepath.Join(
		remoteProjectDirectory,
		fmt.Sprintf("%s%s-%s.", remoteStageNamePrefix, runID, runAttempt),
	)
}

func remoteStageSuffix(runID, runAttempt, stage string) (string, bool) {
	if !runIDPattern.MatchString(runID) || !attemptPattern.MatchString(runAttempt) || filepath.Clean(stage) != stage {
		return "", false
	}
	prefix := remoteStagePrefix(runID, runAttempt)
	suffix := strings.TrimPrefix(stage, prefix)
	return suffix, strings.HasPrefix(stage, prefix) && len(suffix) == 10 && remoteSuffixPattern.MatchString(suffix)
}

func remoteStateDirectory(runID, runAttempt, suffix string) string {
	return filepath.Join(
		"/tmp",
		fmt.Sprintf("%s%s-%s.%s", remoteStateNamePrefix, runID, runAttempt, suffix),
	)
}

func remoteStateDirectoryForStage(request Request, stage string) (string, bool) {
	suffix, valid := remoteStageSuffix(request.RunID, request.RunAttempt, stage)
	if !valid {
		return "", false
	}
	return remoteStateDirectory(request.RunID, request.RunAttempt, suffix), true
}

func validateOwnedPrivateDirectory(path string, allowAbsent bool) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errCommandFailed
	}
	info, err := os.Lstat(path)
	if allowAbsent && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || fileOwner(info) != os.Geteuid() {
		return errCommandFailed
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || realPath != path {
		return errCommandFailed
	}
	return nil
}

func (session remoteSession) execute(ctx context.Context) int {
	if ctx == nil {
		ctx = context.Background()
	}
	preserveState := false
	cleanupRequired := session.request.mode == remoteMint
	status := ExitSuccess

	switch session.request.mode {
	case remotePrepare:
		if err := session.prepare(ctx); err != nil {
			status = operationExitCode(ctx)
		}
	case remoteMint:
		if stage := session.mint(ctx); stage != remoteMintSucceeded {
			status = remoteMintExitCode(ctx, stage)
		} else {
			preserveState = true
		}
	case remoteCleanup:
		cleanupContext, cancel := context.WithTimeout(context.Background(), remoteCleanupTimeout)
		if err := session.cleanupAll(cleanupContext); err != nil {
			status = ExitInvalidInvocation
		} else if contextStopped(ctx) {
			status = contextExitCode(ctx)
		}
		cancel()
	default:
		status = ExitInvalidInvocation
	}

	if cleanupRequired {
		cleanupContext, cancel := context.WithTimeout(context.Background(), remoteCleanupTimeout)
		cleanupErr := session.cleanupMaterial(cleanupContext, preserveState)
		if cleanupErr != nil && preserveState {
			// A failed post-mint container attestation makes the host copy
			// ineligible for transfer. Reconcile again without preserving it.
			_ = session.cleanupMaterial(cleanupContext, false)
		}
		cancel()
		if cleanupErr != nil {
			// Cleanup uncertainty keeps precedence over both the primary mint
			// stage and an interrupted controller context.
			status = remoteMintExitCode(context.Background(), remoteMintRecoveryCleanup)
		}
	}
	return status
}

func (session remoteSession) prepare(ctx context.Context) error {
	var result error
	if err := session.cleanupMaterial(ctx, false); err != nil {
		result = errCommandFailed
	}
	if err := session.sweepLegacyMaterials(); err != nil {
		result = errCommandFailed
	}
	if err := session.sweepContainerStates(ctx); err != nil {
		result = errCommandFailed
	}
	if err := session.sweepStaleStages(ctx); err != nil {
		result = errCommandFailed
	}
	if err := session.sweepLegacyStages(ctx); err != nil {
		result = errCommandFailed
	}
	if err := session.sweepStaleStateDirectories(); err != nil {
		result = errCommandFailed
	}
	if !session.compose(ctx, "exec", "-T", "api", "/app/scribe-browser-session", "--reserve", session.request.containerStatePath) {
		result = errCommandFailed
	}
	return result
}

func (session remoteSession) sweepLegacyMaterials() error {
	entries, err := os.ReadDir("/tmp")
	if err != nil {
		return errCommandFailed
	}
	matched := 0
	for _, entry := range entries {
		if !legacyMaterialPattern.MatchString(entry.Name()) {
			continue
		}
		matched++
		if matched > maximumStaleMaterials {
			return errCommandFailed
		}
		path := filepath.Join("/tmp", entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil || (!info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0) || fileOwner(info) != os.Geteuid() {
			return errCommandFailed
		}
		if err := os.Remove(path); err != nil {
			return errCommandFailed
		}
	}
	return nil
}

func (session remoteSession) mint(ctx context.Context) remoteMintStage {
	if err := removeIfFileOrSymlink(session.request.statePath); err != nil {
		return remoteMintHostStateContract
	}
	if !session.compose(ctx, "exec", "-T", "api", "/app/scribe-browser-session", "--reserved-output", session.request.containerStatePath) {
		return remoteMintSessionCommand
	}
	if err := session.exportState(ctx); err != nil {
		return remoteMintStateExport
	}
	// Delete container material before host validation or transfer. Cleanup
	// repeats this operation after every mint outcome.
	if err := session.removeContainerState(ctx); err != nil {
		return remoteMintContainerCleanup
	}
	info, err := os.Lstat(session.request.statePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return remoteMintHostStateContract
	}
	if err := os.Chmod(session.request.statePath, 0o600); err != nil {
		return remoteMintHostStateContract
	}
	info, err = os.Lstat(session.request.statePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || fileOwner(info) != os.Geteuid() || info.Size() < minimumStateBytes || info.Size() > maximumStateBytes {
		return remoteMintHostStateContract
	}
	return remoteMintSucceeded
}

func (session remoteSession) exportState(ctx context.Context) (returnErr error) {
	if filepath.Dir(session.request.statePath) != session.request.stateDirectory ||
		filepath.Base(session.request.statePath) != "storage-state.json" {
		return errCommandFailed
	}
	root, err := os.OpenRoot(session.request.stateDirectory)
	if err != nil {
		return errCommandFailed
	}
	defer func() {
		if closeErr := root.Close(); returnErr != nil && closeErr != nil {
			returnErr = errors.Join(returnErr, errCommandFailed)
		}
	}()

	file, err := root.OpenFile(
		"storage-state.json",
		os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return errCommandFailed
	}
	preserve := false
	defer func() {
		if file != nil {
			if closeErr := file.Close(); returnErr != nil && closeErr != nil {
				returnErr = errors.Join(returnErr, errCommandFailed)
			}
		}
		if !preserve {
			if removeErr := root.Remove("storage-state.json"); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, errCommandFailed)
			}
		}
	}()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || fileOwner(info) != os.Geteuid() {
		return errCommandFailed
	}
	result := session.run.RunStream(
		ctx,
		session.request.dockerExecutable,
		session.composeArgs("exec", "-T", "api", "/app/scribe-browser-session", "--export", session.request.containerStatePath),
		map[string]string{"DOCKER_CONFIG": remoteDockerConfig},
		remoteCommandTimeout,
		commandKillGrace,
		file,
		maximumStateBytes,
	)
	if result.err != nil || result.exitCode != 0 {
		return errCommandFailed
	}
	if err := file.Sync(); err != nil {
		return errCommandFailed
	}
	if err := file.Close(); err != nil {
		file = nil
		return errCommandFailed
	}
	file = nil
	preserve = true
	return nil
}

func (session remoteSession) cleanupAll(ctx context.Context) error {
	if err := session.cleanupMaterial(ctx, false); err != nil {
		return errCommandFailed
	}
	if err := removeRemoteStateDirectory(session.request.stateDirectory); err != nil {
		return errCommandFailed
	}
	return nil
}

func (session remoteSession) cleanupMaterial(ctx context.Context, preserveState bool) error {
	result := session.removeContainerState(ctx)
	if !preserveState {
		if err := removeIfFileOrSymlink(session.request.statePath); err != nil {
			result = errCommandFailed
		}
	}
	return result
}

func (session remoteSession) removeContainerState(ctx context.Context) error {
	if !session.compose(ctx, "exec", "-T", "api", "/app/scribe-browser-session", "--cleanup", session.request.containerStatePath) {
		return errCommandFailed
	}
	return nil
}

func (session remoteSession) sweepContainerStates(ctx context.Context) error {
	if !session.compose(ctx, "exec", "-T", "api", "/app/scribe-browser-session", "--cleanup-all") {
		return errCommandFailed
	}
	return nil
}

func (session remoteSession) sweepStaleStages(ctx context.Context) error {
	return session.sweepStageRoot(ctx, filepath.Dir(session.request.stageDirectory), staleStagePattern, false)
}

func (session remoteSession) sweepLegacyStages(ctx context.Context) error {
	return session.sweepStageRoot(ctx, "/tmp", legacyStagePattern, true)
}

func (session remoteSession) sweepStageRoot(ctx context.Context, root string, pattern *regexp.Regexp, legacy bool) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return errCommandFailed
	}
	matched := 0
	for _, entry := range entries {
		matches := pattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if path == session.request.stageDirectory {
			continue
		}
		matched++
		if matched > maximumStaleMaterials {
			return errCommandFailed
		}
		if err := validateOwnedPrivateDirectory(path, false); err != nil {
			return errCommandFailed
		}
		staleLock, lockErr := acquireRemoteLock(ctx, path)
		if lockErr != nil {
			return errCommandFailed
		}
		if err := validateOwnedPrivateDirectory(path, false); err != nil {
			_ = staleLock.release()
			return errCommandFailed
		}
		children, readErr := os.ReadDir(path)
		if readErr != nil {
			_ = staleLock.release()
			return errCommandFailed
		}
		if len(children) > 4 {
			_ = staleLock.release()
			return errCommandFailed
		}
		for _, child := range children {
			switch child.Name() {
			case remoteBinaryName, ".session.lock":
				if child.Name() == ".session.lock" {
					continue
				}
				if err := removeIfFileOrSymlink(filepath.Join(path, child.Name())); err != nil {
					_ = staleLock.release()
					return errCommandFailed
				}
			case "storage-state.json", "helper.sh":
				if !legacy {
					_ = staleLock.release()
					return errCommandFailed
				}
				if err := removeIfFileOrSymlink(filepath.Join(path, child.Name())); err != nil {
					_ = staleLock.release()
					return errCommandFailed
				}
			default:
				_ = staleLock.release()
				return errCommandFailed
			}
		}
		if !legacy {
			if len(matches) != 4 || removeRemoteStateDirectory(remoteStateDirectory(matches[1], matches[2], matches[3])) != nil {
				_ = staleLock.release()
				return errCommandFailed
			}
		}
		if err := staleLock.finalize(path); err != nil {
			return errCommandFailed
		}
	}
	return nil
}

func (session remoteSession) sweepStaleStateDirectories() error {
	entries, err := os.ReadDir("/tmp")
	if err != nil {
		return errCommandFailed
	}
	matched := 0
	for _, entry := range entries {
		if !remoteStatePattern.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join("/tmp", entry.Name())
		if path == session.request.stateDirectory {
			continue
		}
		matched++
		if matched > maximumStaleMaterials || removeRemoteStateDirectory(path) != nil {
			return errCommandFailed
		}
	}
	return nil
}

func removeRemoteStateDirectory(path string) error {
	if err := validateOwnedPrivateDirectory(path, true); err != nil {
		return errCommandFailed
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return errCommandFailed
	}
	children, err := os.ReadDir(path)
	if err != nil || len(children) > 1 {
		return errCommandFailed
	}
	if len(children) == 1 {
		if children[0].Name() != "storage-state.json" || removeIfFileOrSymlink(filepath.Join(path, children[0].Name())) != nil {
			return errCommandFailed
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errCommandFailed
	}
	return nil
}

func (session remoteSession) compose(ctx context.Context, args ...string) bool {
	result := session.run.Run(
		ctx,
		session.request.dockerExecutable,
		session.composeArgs(args...),
		map[string]string{"DOCKER_CONFIG": remoteDockerConfig},
		remoteCommandTimeout,
		commandKillGrace,
		maximumCommandOutput,
	)
	return result.err == nil && result.exitCode == 0
}

func (session remoteSession) composeArgs(args ...string) []string {
	prefix := []string{
		"compose",
		"--project-directory", remoteProjectDirectory,
		"--project-name", remoteComposeProject,
		"-f", filepath.Join(remoteProjectDirectory, "docker-compose.yaml"),
		"-f", remoteComposeOverlay,
	}
	return append(prefix, args...)
}

func removeIfFileOrSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || (!info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0) {
		return errCommandFailed
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errCommandFailed
	}
	return nil
}

func fileSHA256(path string, maximum int64) (string, error) {
	file, err := openScopedFile(path)
	if err != nil {
		return "", errCommandFailed
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil || written > maximum {
		return "", errCommandFailed
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func openScopedFile(path string) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errCommandFailed
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, errCommandFailed
	}
	file, openErr := root.Open(filepath.Base(path))
	closeErr := root.Close()
	if openErr != nil || closeErr != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, errCommandFailed
	}
	return file, nil
}

func fileOwner(info os.FileInfo) int {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(stat.Uid)
}

type remoteLock struct {
	file *os.File
	path string
}

func acquireRemoteLock(ctx context.Context, stageDirectory string) (*remoteLock, error) {
	return acquireFileLock(ctx, filepath.Join(stageDirectory, ".session.lock"))
}

func acquireFileLock(ctx context.Context, path string) (*remoteLock, error) {
	// #nosec G302 -- this credential lifecycle lock is deliberately private.
	descriptor, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, errCommandFailed
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = syscall.Close(descriptor)
		return nil, errCommandFailed
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || fileOwner(info) != os.Geteuid() {
		_ = file.Close()
		return nil, errCommandFailed
	}
	for {
		err = syscall.Flock(descriptor, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &remoteLock{file: file, path: path}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, errCommandFailed
		}
		if waitErr := waitForTimer(ctx, remoteLockPoll); waitErr != nil {
			_ = file.Close()
			return nil, errCommandFailed
		}
	}
}

func (lock *remoteLock) release() error {
	if lock == nil || lock.file == nil {
		return errCommandFailed
	}
	descriptor := int(lock.file.Fd())
	unlockErr := syscall.Flock(descriptor, syscall.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	if unlockErr != nil || closeErr != nil {
		return errCommandFailed
	}
	return nil
}

func (lock *remoteLock) finalize(stageDirectory string) error {
	if lock == nil || lock.file == nil || lock.path != filepath.Join(stageDirectory, ".session.lock") {
		return errCommandFailed
	}
	if err := os.Remove(lock.path); err != nil {
		_ = lock.release()
		return errCommandFailed
	}
	if err := os.Remove(stageDirectory); err != nil {
		_ = lock.release()
		return errCommandFailed
	}
	return lock.release()
}

func operationExitCode(ctx context.Context) int {
	if contextStopped(ctx) {
		return contextExitCode(ctx)
	}
	return ExitInvalidInvocation
}
