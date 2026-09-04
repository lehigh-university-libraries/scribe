package productionbrowserreadiness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	// ExitSuccess indicates the full readiness and credential cleanup contract passed.
	ExitSuccess = 0
	// ExitInvalidInvocation is also used for fail-closed transport failures.
	ExitInvalidInvocation = 2
	// ExitInterrupted preserves SIGINT after successful reconciliation.
	ExitInterrupted = 130
	// ExitTerminated preserves SIGTERM after successful reconciliation.
	ExitTerminated = 143

	minimumRemainingSession  = 2460 * time.Second
	maximumRemainingSession  = 3000 * time.Second
	cleanupTimeout           = 50 * time.Minute
	secretSettlementTimeout  = 8 * time.Minute
	secretSettlementDelay    = 10 * time.Second
	secretSettlementAttempts = 49
)

var (
	storageValuePattern           = regexp.MustCompile(`^[A-Za-z0-9_-]{64}$`)
	storageDomainPattern          = regexp.MustCompile(`^\.?[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
	errSecretPlaceholderUnsettled = errors.New("secret manager placeholder observation unsettled")
	errSecretVersionSetUnsettled  = errors.New("secret manager version set unsettled")
)

// Result is the complete transport outcome.
type Result struct {
	ExitCode int
	Category string
}

// RunnerOption customizes deterministic runner boundaries for focused tests.
type RunnerOption func(*Runner)

// WithRandomSource supplies stage-name entropy.
func WithRandomSource(source io.Reader) RunnerOption {
	return func(runner *Runner) {
		if source != nil {
			runner.random = source
		}
	}
}

// WithClock supplies the time source used for cookie-lifetime fencing.
func WithClock(now func() time.Time) RunnerOption {
	return func(runner *Runner) {
		if now != nil {
			runner.now = now
		}
	}
}

// WithWait supplies the bounded settlement wait used between observations.
func WithWait(wait func(context.Context, time.Duration) error) RunnerOption {
	return func(runner *Runner) {
		if wait != nil {
			runner.wait = wait
		}
	}
}

// Runner owns production credential transport, binding, and reconciliation.
type Runner struct {
	client TransportClient
	random io.Reader
	now    func() time.Time
	wait   func(context.Context, time.Duration) error
}

// NewRunner constructs a production browser lifecycle runner.
func NewRunner(client TransportClient, options ...RunnerOption) *Runner {
	runner := &Runner{client: client, random: rand.Reader, now: time.Now, wait: waitForTimer}
	for _, option := range options {
		if option != nil {
			option(runner)
		}
	}
	return runner
}

type lifecycleState struct {
	request               Request
	files                 LocalFiles
	temporaryDirectory    string
	transportDigest       string
	remoteStage           string
	remotePossible        bool
	remoteInvoked         bool
	jobRestoreRequired    bool
	secretAddPossible     bool
	secretIdentityUnknown bool
	cleanupVersion        string
}

// Run executes the transport and returns only redacted categorical failures.
func (runner *Runner) Run(ctx context.Context, request Request, stdout, stderr io.Writer) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if err := ValidateRequest(request); err != nil || runner == nil || runner.client == nil || runner.random == nil || runner.now == nil || runner.wait == nil {
		_, _ = fmt.Fprintln(stderr, "Production browser readiness transport failed: invalid-request.")
		return Result{ExitCode: ExitInvalidInvocation, Category: "invalid-request"}
	}

	state, err := prepareLifecycleState(request)
	if err != nil {
		return runner.finish(ctx, &lifecycleState{request: request}, "temporary-state", stdout, stderr)
	}
	category := runner.runLifecycle(ctx, state)
	return runner.finish(ctx, state, category, stdout, stderr)
}

func (runner *Runner) runLifecycle(ctx context.Context, state *lifecycleState) string {
	request := state.request
	if err := copyTransportBinary(request.TransportExecutable, state.files.TransportBinary); err != nil {
		return "transport-copy"
	}
	digest, err := fileSHA256(state.files.TransportBinary, maximumTransportBytes)
	if err != nil {
		return "transport-digest"
	}
	state.transportDigest = digest

	if err := runner.client.Preflight(ctx, request); err != nil {
		return operationCategory(ctx, "browser-preflight")
	}
	state.jobRestoreRequired = true
	if err := runner.client.SetJobSecretVersion(ctx, request, "1"); err != nil {
		return operationCategory(ctx, "job-placeholder")
	}
	if err := runner.reconcileSecretVersions(ctx, request); err != nil {
		return secretReconciliationCategory(ctx, "stale-secret-cleanup", err)
	}
	if err := runner.client.GenerateSSHKey(ctx, request, state.files); err != nil {
		return operationCategory(ctx, "ssh-key")
	}
	stage, err := runner.remoteStage(request)
	if err != nil {
		return "remote-stage-identity"
	}
	state.remoteStage = stage
	state.remotePossible = true
	if err := runner.client.CreateRemoteStage(ctx, request, state.files, stage); err != nil {
		return operationCategory(ctx, "remote-stage-create")
	}
	if err := runner.client.CopyTransport(ctx, request, state.files, stage); err != nil {
		return operationCategory(ctx, "remote-transport-copy")
	}
	state.remoteInvoked = true
	if err := runner.client.InvokeRemote(ctx, request, state.files, stage, digest, remotePrepare); err != nil {
		return operationCategory(ctx, "remote-preclean")
	}
	if err := runner.client.InvokeRemote(ctx, request, state.files, stage, digest, remoteMint); err != nil {
		return remoteMintCategory(ctx, err)
	}
	if err := runner.client.CopyRemoteState(ctx, request, state.files, stage); err != nil {
		return operationCategory(ctx, "state-copy")
	}
	if err := runner.cleanupRemote(state); err != nil {
		return operationCategory(ctx, "remote-cleanup")
	}
	state.remotePossible = false

	storageState, stateDigest, err := validateStorageState(state.files.StorageState, runner.now())
	if err != nil {
		return "state-contract"
	}
	state.secretAddPossible = true
	version, addErr := runner.client.AddSecretVersion(ctx, request, state.files.StorageState)
	if versionPattern.MatchString(version) {
		state.cleanupVersion = version
	}
	if addErr != nil {
		state.secretIdentityUnknown = state.cleanupVersion == ""
		return operationCategory(ctx, "secret-version-create")
	}
	if state.cleanupVersion == "" {
		return "secret-version-identity"
	}
	secretState, err := runner.client.SecretVersionState(ctx, request, state.cleanupVersion)
	if err != nil || secretState != SecretEnabled {
		return operationCategory(ctx, "secret-version-attestation")
	}
	if err := runner.client.SetJobSecretVersion(ctx, request, state.cleanupVersion); err != nil {
		return operationCategory(ctx, "job-credential")
	}
	if storageState.cookieExpiry < float64(runner.now().Add(minimumRemainingSession).Unix()) {
		return "state-expiry-after-binding"
	}
	if err := removeLocalState(state.files.StorageState); err != nil {
		return "local-state-cleanup"
	}
	if err := runner.client.RunReadiness(ctx, request, state.cleanupVersion, stateDigest); err != nil {
		return browserExecutionCategory(ctx, err)
	}
	if err := runner.client.SetJobSecretVersion(ctx, request, "1"); err != nil {
		return operationCategory(ctx, "job-secret-restore")
	}
	if err := runner.client.DestroySecretVersion(ctx, request, state.cleanupVersion); err != nil {
		return operationCategory(ctx, "exact-secret-version")
	}
	state.cleanupVersion = ""
	if err := runner.reconcileSecretVersions(ctx, request); err != nil {
		return secretReconciliationCategory(ctx, "observed-secret-cleanup", err)
	}
	state.secretAddPossible = false
	state.jobRestoreRequired = false
	return ""
}

func browserExecutionCategory(ctx context.Context, err error) string {
	if contextStopped(ctx) {
		return operationCategory(ctx, "browser-execution")
	}
	var failure browserReadinessFailure
	if errors.As(err, &failure) && failure.category != "" {
		return "browser-execution-" + failure.category
	}
	return "browser-execution"
}

func (runner *Runner) finish(
	ctx context.Context,
	state *lifecycleState,
	category string,
	stdout, stderr io.Writer,
) Result {
	cleanupFailed := false
	if state.temporaryDirectory != "" {
		cleanupContext, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		cleanupFailed = runner.cleanup(cleanupContext, state, stderr)
		cancel()
	}
	if cleanupFailed {
		if category != "" {
			_, _ = fmt.Fprintf(stderr, "Production browser readiness primary failure: %s.\n", category)
		}
		category = "cleanup-unconfirmed"
	}
	if category == "" && !cleanupFailed {
		_, _ = fmt.Fprintf(stdout, "Production browser readiness passed for %s.\n", state.request.Job)
		return Result{ExitCode: ExitSuccess}
	}
	if category == "" {
		category = "cleanup-unconfirmed"
	}
	exitCode := ExitInvalidInvocation
	if !cleanupFailed && contextStopped(ctx) {
		exitCode = contextExitCode(ctx)
		category = "interrupted"
	}
	_, _ = fmt.Fprintf(stderr, "Production browser readiness transport failed: %s.\n", category)
	return Result{ExitCode: exitCode, Category: category}
}

func (runner *Runner) cleanup(ctx context.Context, state *lifecycleState, stderr io.Writer) bool {
	failed := false
	if err := removeLocalState(state.files.StorageState); err != nil {
		_, _ = fmt.Fprintln(stderr, "Production browser readiness cleanup failed: local-state.")
		failed = true
	}
	if state.remotePossible {
		if err := runner.cleanupRemoteWithContext(ctx, state); err != nil {
			_, _ = fmt.Fprintln(stderr, "Production browser readiness cleanup failed: remote-state.")
			failed = true
		} else {
			state.remotePossible = false
		}
	}
	if state.jobRestoreRequired {
		if err := runner.client.SetJobSecretVersion(ctx, state.request, "1"); err != nil {
			_, _ = fmt.Fprintln(stderr, "Production browser readiness cleanup failed: job-secret-restore.")
			failed = true
		}
	}
	if state.cleanupVersion != "" {
		if err := runner.client.DestroySecretVersion(ctx, state.request, state.cleanupVersion); err != nil {
			_, _ = fmt.Fprintln(stderr, "Production browser readiness cleanup failed: exact-secret-version.")
			failed = true
		} else {
			state.cleanupVersion = ""
		}
	}
	if state.secretAddPossible {
		if err := runner.reconcileSecretVersions(ctx, state.request); err != nil {
			_, _ = fmt.Fprintln(stderr, "Production browser readiness cleanup failed: observed-secret-versions.")
			failed = true
		}
		if state.secretIdentityUnknown {
			_, _ = fmt.Fprintln(stderr, "Production browser readiness cleanup failed: secret-version-identity.")
			failed = true
		}
	}
	if err := removeTemporaryDirectory(state.temporaryDirectory, state.request.TemporaryRoot); err != nil {
		_, _ = fmt.Fprintln(stderr, "Production browser readiness cleanup failed: temporary-state.")
		failed = true
	}
	return failed
}

func (runner *Runner) reconcileSecretVersions(ctx context.Context, request Request) error {
	settlementContext, cancel := context.WithTimeout(ctx, secretSettlementTimeout)
	defer cancel()
	lastError := errCommandFailed
	for attempt := 0; attempt < secretSettlementAttempts; attempt++ {
		settled, attemptError := runner.reconcileSecretVersionsOnce(settlementContext, request)
		if settled {
			return nil
		}
		if !retryableSecretReconciliationError(attemptError) {
			return errCommandFailed
		}
		lastError = attemptError
		if attempt+1 == secretSettlementAttempts {
			return lastError
		}
		if err := runner.wait(settlementContext, secretSettlementDelay); err != nil {
			if contextStopped(ctx) {
				return errCommandFailed
			}
			return lastError
		}
	}
	return lastError
}

func (runner *Runner) reconcileSecretVersionsOnce(ctx context.Context, request Request) (bool, error) {
	versions, err := runner.client.ListSecretVersions(ctx, request)
	if err != nil {
		if errors.Is(err, errSecretInventoryUnavailable) {
			return false, errSecretInventoryUnavailable
		}
		return false, errCommandFailed
	}
	placeholderSeen, err := validateSecretInventory(versions)
	if err != nil {
		return false, errCommandFailed
	}
	if !placeholderSeen {
		return false, errSecretPlaceholderUnsettled
	}
	for _, version := range versions {
		if version.Version == "1" {
			continue
		}
		if err := runner.client.DestroySecretVersion(ctx, request, version.Version); err != nil {
			return false, errSecretVersionSetUnsettled
		}
	}
	remaining, err := runner.client.ListSecretVersions(ctx, request)
	if err != nil {
		if errors.Is(err, errSecretInventoryUnavailable) {
			return false, errSecretInventoryUnavailable
		}
		return false, errCommandFailed
	}
	placeholderSeen, err = validateSecretInventory(remaining)
	if err != nil {
		return false, errCommandFailed
	}
	if !placeholderSeen {
		return false, errSecretPlaceholderUnsettled
	}
	if len(remaining) != 1 {
		return false, errSecretVersionSetUnsettled
	}
	return true, nil
}

func retryableSecretReconciliationError(err error) bool {
	return errors.Is(err, errSecretInventoryUnavailable) ||
		errors.Is(err, errSecretPlaceholderUnsettled) ||
		errors.Is(err, errSecretVersionSetUnsettled)
}

func secretReconciliationCategory(ctx context.Context, base string, err error) string {
	if contextStopped(ctx) {
		return "interrupted"
	}
	switch {
	case errors.Is(err, errSecretInventoryUnavailable):
		return base + "-inventory-unavailable"
	case errors.Is(err, errSecretPlaceholderUnsettled):
		return base + "-placeholder-unsettled"
	case errors.Is(err, errSecretVersionSetUnsettled):
		return base + "-versions-unsettled"
	default:
		return base
	}
}

func validateSecretInventory(versions []SecretVersion) (bool, error) {
	if len(versions) > maximumSecretVersions {
		return false, errCommandFailed
	}
	placeholderSeen := false
	seen := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		if _, duplicate := seen[version.Version]; duplicate {
			return false, errCommandFailed
		}
		seen[version.Version] = struct{}{}
		if version.Version == "1" {
			if version.State == SecretDisabled {
				continue
			}
			if version.State != SecretEnabled {
				return false, errCommandFailed
			}
			placeholderSeen = true
			continue
		}
		if !versionPattern.MatchString(version.Version) || (version.State != SecretEnabled && version.State != SecretDisabled) {
			return false, errCommandFailed
		}
	}
	return placeholderSeen, nil
}

func (runner *Runner) cleanupRemote(state *lifecycleState) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	return runner.cleanupRemoteWithContext(ctx, state)
}

func (runner *Runner) cleanupRemoteWithContext(ctx context.Context, state *lifecycleState) error {
	return runner.client.CleanupRemote(
		ctx,
		state.request,
		state.files,
		state.remoteStage,
		state.transportDigest,
		state.remoteInvoked,
	)
}

func (runner *Runner) remoteStage(request Request) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	bytes := make([]byte, 10)
	if _, err := io.ReadFull(runner.random, bytes); err != nil {
		return "", errCommandFailed
	}
	for index := range bytes {
		bytes[index] = alphabet[int(bytes[index])%len(alphabet)]
	}
	return remoteStagePrefix(request.RunID, request.RunAttempt) + string(bytes), nil
}

func prepareLifecycleState(request Request) (*lifecycleState, error) {
	temporaryDirectory, err := os.MkdirTemp(request.TemporaryRoot, "scribe-production-browser.")
	if err != nil {
		return nil, errCommandFailed
	}
	info, err := os.Lstat(temporaryDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || fileOwner(info) != os.Geteuid() {
		_ = os.RemoveAll(temporaryDirectory)
		return nil, errCommandFailed
	}
	return &lifecycleState{
		request:            request,
		temporaryDirectory: temporaryDirectory,
		files: LocalFiles{
			SSHKey:          filepath.Join(temporaryDirectory, "id_ed25519"),
			KnownHosts:      filepath.Join(temporaryDirectory, "known_hosts"),
			TransportBinary: filepath.Join(temporaryDirectory, remoteBinaryName),
			StorageState:    filepath.Join(temporaryDirectory, "storage-state.json"),
		},
	}, nil
}

func copyTransportBinary(source, target string) error {
	sourceInfo, err := os.Lstat(source)
	if err != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return errCommandFailed
	}
	sourceFile, err := openScopedFile(source)
	if err != nil {
		return errCommandFailed
	}
	defer sourceFile.Close()
	openedInfo, err := sourceFile.Stat()
	if err != nil || !os.SameFile(sourceInfo, openedInfo) || openedInfo.Size() <= 0 || openedInfo.Size() > maximumTransportBytes {
		return errCommandFailed
	}
	targetRoot, err := os.OpenRoot(filepath.Dir(target))
	if err != nil {
		return errCommandFailed
	}
	// #nosec G302 -- the copied transport must be executable and remains inside
	// the already-attested private 0700 lifecycle directory.
	targetFile, err := targetRoot.OpenFile(filepath.Base(target), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	rootCloseErr := targetRoot.Close()
	if rootCloseErr != nil {
		if targetFile != nil {
			_ = targetFile.Close()
		}
		_ = os.Remove(target)
		return errCommandFailed
	}
	if err != nil {
		return errCommandFailed
	}
	written, copyErr := io.Copy(targetFile, io.LimitReader(sourceFile, maximumTransportBytes+1))
	syncErr := targetFile.Sync()
	closeErr := targetFile.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written != openedInfo.Size() || written > maximumTransportBytes {
		return errCommandFailed
	}
	targetInfo, err := os.Lstat(target)
	if err != nil || !targetInfo.Mode().IsRegular() || targetInfo.Mode()&os.ModeSymlink != 0 || targetInfo.Mode().Perm() != 0o700 || fileOwner(targetInfo) != os.Geteuid() {
		return errCommandFailed
	}
	return nil
}

type browserStorageState struct {
	Cookies []browserCookie   `json:"cookies"`
	Origins []json.RawMessage `json:"origins"`
}

type browserCookie struct {
	Name     string      `json:"name"`
	Value    string      `json:"value"`
	Domain   string      `json:"domain"`
	Path     string      `json:"path"`
	Expires  json.Number `json:"expires"`
	HTTPOnly bool        `json:"httpOnly"`
	Secure   bool        `json:"secure"`
	SameSite string      `json:"sameSite"`
}

type validatedStorageState struct {
	cookieExpiry float64
}

func validateStorageState(path string, now time.Time) (validatedStorageState, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || fileOwner(info) != os.Geteuid() || info.Size() < minimumStateBytes || info.Size() > maximumStateBytes {
		return validatedStorageState{}, "", errCommandFailed
	}
	file, err := openScopedFile(path)
	if err != nil {
		return validatedStorageState{}, "", errCommandFailed
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return validatedStorageState{}, "", errCommandFailed
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumStateBytes+1))
	if err != nil || len(data) < minimumStateBytes || len(data) > maximumStateBytes {
		return validatedStorageState{}, "", errCommandFailed
	}
	var state browserStorageState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&state); err != nil {
		return validatedStorageState{}, "", errCommandFailed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return validatedStorageState{}, "", errCommandFailed
	}
	if state.Cookies == nil || len(state.Cookies) != 1 || state.Origins == nil || len(state.Origins) != 0 {
		return validatedStorageState{}, "", errCommandFailed
	}
	cookie := state.Cookies[0]
	expiry, err := cookie.Expires.Float64()
	minimum := float64(now.Add(minimumRemainingSession).Unix())
	maximum := float64(now.Add(maximumRemainingSession).Unix())
	if err != nil || math.IsNaN(expiry) || math.IsInf(expiry, 0) || expiry < minimum || expiry > maximum ||
		cookie.Name != "scribe_session" || !storageValuePattern.MatchString(cookie.Value) ||
		!storageDomainPattern.MatchString(cookie.Domain) || cookie.Path != "/" ||
		!cookie.HTTPOnly || !cookie.Secure || cookie.SameSite != "Lax" {
		return validatedStorageState{}, "", errCommandFailed
	}
	digest, err := fileSHA256(path, maximumStateBytes)
	if err != nil {
		return validatedStorageState{}, "", errCommandFailed
	}
	return validatedStorageState{cookieExpiry: expiry}, digest, nil
}

func removeLocalState(path string) error {
	if path == "" {
		return nil
	}
	if err := removeIfFileOrSymlink(path); err != nil {
		return errCommandFailed
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errCommandFailed
	}
	return nil
}

func removeTemporaryDirectory(path, parent string) error {
	if path == "" {
		return nil
	}
	relative, err := filepath.Rel(parent, path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || len(relative) < len("scribe-production-browser.") || relative[:len("scribe-production-browser.")] != "scribe-production-browser." {
		return errCommandFailed
	}
	if err := os.RemoveAll(path); err != nil {
		return errCommandFailed
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errCommandFailed
	}
	return nil
}

// SignalCause creates the typed cancellation cause preserved after cleanup.
func SignalCause(exitCode int) error {
	if exitCode != ExitInterrupted && exitCode != ExitTerminated {
		exitCode = ExitInterrupted
	}
	return signalCause{exitCode: exitCode}
}

type signalCause struct {
	exitCode int
}

func (cause signalCause) Error() string { return "production browser readiness interrupted" }

func contextStopped(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}

func contextExitCode(ctx context.Context) int {
	var cause signalCause
	if ctx != nil && errors.As(context.Cause(ctx), &cause) {
		return cause.exitCode
	}
	return ExitInterrupted
}

func operationCategory(ctx context.Context, category string) string {
	if contextStopped(ctx) {
		return "interrupted"
	}
	return category
}

func waitForTimer(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
