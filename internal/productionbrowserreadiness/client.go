package productionbrowserreadiness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	secretCommandTimeout             = 30 * time.Second
	jobUpdateTimeout                 = 180 * time.Second
	jobAttestTimeout                 = 30 * time.Second
	remoteCallTimeout                = 180 * time.Second
	remotePrepareCallTimeout         = 9 * time.Minute
	remoteMintCallTimeout            = 14 * time.Minute
	remoteCleanupCallTimeout         = 7 * time.Minute
	readinessTimeout                 = 50 * time.Minute
	readinessKillGrace               = 46 * time.Minute
	maximumReadinessDiagnosticsBytes = 128 << 10
	commandKillGrace                 = 5 * time.Second
	jobUpdateAttempts                = 3
	jobAttestAttempts                = 3
	destroyAttempts                  = 5
	remoteCleanupAttempts            = 3
	maximumSecretVersions            = 64
	browserStateVariable             = "SCRIBE_BROWSER_STORAGE_STATE_JSON"
)

var (
	versionPattern                = regexp.MustCompile(`^([2-9]|[1-9][0-9]{1,19})$`)
	anyVersionPattern             = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	numericProjectPattern         = regexp.MustCompile(`^[1-9][0-9]{5,19}$`)
	errSecretInventoryUnavailable = errors.New("secret manager inventory unavailable")
	browserTaskDiagnosticPattern  = regexp.MustCompile(`^\[task\] index=(?:unknown|[0-9]+) retried=(?:unknown|[0-9]+) exit_code=([0-9]+) term_signal=(?:unknown|[0-9]+) status_code=(?:unknown|[0-9]+)$`)
)

var manifestTaskFailureCategories = [...]string{
	"library-navigation",
	"import-form",
	"import-request",
	"import-contract",
	"editor-navigation",
	"editor-mount",
	"first-canvas",
	"first-image",
	"first-annotations",
	"first-publication",
	"second-image",
	"second-canvas",
	"second-annotations",
	"second-overlay",
	"second-publication",
}

type browserReadinessFailure struct {
	category string
}

func (failure browserReadinessFailure) Error() string {
	return "production browser readiness execution failed"
}

// SecretState is the canonical Secret Manager API version state.
type SecretState string

const (
	SecretEnabled   SecretState = "ENABLED"
	SecretDisabled  SecretState = "DISABLED"
	SecretDestroyed SecretState = "DESTROYED"
)

// SecretVersion is one exact, scoped Secret Manager version record.
type SecretVersion struct {
	Version string
	State   SecretState
}

// LocalFiles identifies private controller artifacts.
type LocalFiles struct {
	SSHKey          string
	KnownHosts      string
	TransportBinary string
	StorageState    string
}

// TransportClient is the typed external boundary used by Runner.
type TransportClient interface {
	Preflight(context.Context, Request) error
	SetJobSecretVersion(context.Context, Request, string) error
	ListSecretVersions(context.Context, Request) ([]SecretVersion, error)
	SecretVersionState(context.Context, Request, string) (SecretState, error)
	AddSecretVersion(context.Context, Request, string) (string, error)
	DestroySecretVersion(context.Context, Request, string) error
	GenerateSSHKey(context.Context, Request, LocalFiles) error
	CreateRemoteStage(context.Context, Request, LocalFiles, string) error
	CopyTransport(context.Context, Request, LocalFiles, string) error
	InvokeRemote(context.Context, Request, LocalFiles, string, string, remoteMode) error
	CopyRemoteState(context.Context, Request, LocalFiles, string) error
	CleanupRemote(context.Context, Request, LocalFiles, string, string, bool) error
	RunReadiness(context.Context, Request, string, string) error
}

// GCloudConfig configures the direct-exec production transport adapter.
type GCloudConfig struct {
	GCloudExecutable    string
	SSHKeygenExecutable string
	Wait                func(context.Context, time.Duration) error
}

// GCloudClient invokes the Secret Manager metadata API, gcloud, ssh-keygen,
// and the typed Cloud Run helper through bounded, non-logging boundaries.
type GCloudClient struct {
	gcloud          string
	sshKeygen       string
	wait            func(context.Context, time.Duration) error
	run             commandRunner
	secretInventory secretInventoryAPI
}

// NewGCloudClient resolves each local executable once before any mutation.
func NewGCloudClient(config GCloudConfig) (*GCloudClient, error) {
	if config.GCloudExecutable == "" {
		config.GCloudExecutable = "gcloud"
	}
	if config.SSHKeygenExecutable == "" {
		config.SSHKeygenExecutable = "ssh-keygen"
	}
	gcloud, err := resolveExecutable(config.GCloudExecutable)
	if err != nil {
		return nil, &ValidationError{Field: "gcloud executable", Rule: "must resolve to an executable regular file"}
	}
	sshKeygen, err := resolveExecutable(config.SSHKeygenExecutable)
	if err != nil {
		return nil, &ValidationError{Field: "ssh-keygen executable", Rule: "must resolve to an executable regular file"}
	}
	if config.Wait == nil {
		config.Wait = waitForTimer
	}
	initializationContext, initializationCancel := context.WithTimeout(context.Background(), secretCommandTimeout)
	secretInventory, err := newSecretInventoryAPI(initializationContext)
	initializationCancel()
	if err != nil {
		return nil, &ValidationError{Field: "Secret Manager inventory client", Rule: "must initialize with application default credentials"}
	}
	return &GCloudClient{
		gcloud:          gcloud,
		sshKeygen:       sshKeygen,
		wait:            config.Wait,
		run:             osCommandRunner{},
		secretInventory: secretInventory,
	}, nil
}

// Preflight fences prior browser executions through the existing typed Cloud
// Run lifecycle helper.
func (client *GCloudClient) Preflight(ctx context.Context, request Request) error {
	result := client.run.Run(
		ctx,
		request.CloudReadinessExecutable,
		[]string{"--preflight-only", request.Job, "browser"},
		map[string]string{
			"GCLOUD_PROJECT": request.Project,
			"SCRIBE_REGION":  request.Region,
		},
		readinessTimeout,
		readinessKillGrace,
		maximumCommandOutput,
	)
	if result.err != nil || result.exitCode != 0 {
		return errCommandFailed
	}
	return nil
}

func browserTaskFailureCategory(path string) string {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return ""
	}
	defer root.Close()
	target := filepath.Base(path)
	info, err := root.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumReadinessDiagnosticsBytes {
		return ""
	}
	file, err := root.Open(target)
	if err != nil {
		return ""
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumReadinessDiagnosticsBytes+1))
	if err != nil || len(data) == 0 || len(data) > maximumReadinessDiagnosticsBytes {
		return ""
	}

	category := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		matches := browserTaskDiagnosticPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			continue
		}
		exitCode, err := strconv.Atoi(matches[1])
		if err != nil {
			return ""
		}
		candidate := ""
		switch {
		case exitCode == 35:
			candidate = "manifest"
		case exitCode >= 75 && exitCode < 75+len(manifestTaskFailureCategories):
			candidate = "manifest-" + manifestTaskFailureCategories[exitCode-75]
		}
		if candidate == "" {
			continue
		}
		if category != "" {
			return ""
		}
		category = candidate
	}
	return category
}

// SetJobSecretVersion completes the desired mutation and then independently
// attests the exact job binding. An ambiguous update is retried with the same
// desired version; a pre-existing matching describe is never treated as proof
// that the mutation settled.
func (client *GCloudClient) SetJobSecretVersion(ctx context.Context, request Request, version string) error {
	if !anyVersionPattern.MatchString(version) {
		return errCommandFailed
	}
	mutationSettled := false
	for attempt := 0; attempt < jobUpdateAttempts; attempt++ {
		update := client.gcloudCommand(ctx, jobUpdateTimeout,
			"run", "jobs", "update", request.Job,
			"--project", request.Project,
			"--region", request.Region,
			"--update-secrets="+browserStateVariable+"="+request.Secret+":"+version,
			"--format=json[no-transforms]",
			"--quiet",
		)
		if update.err == nil && update.exitCode == 0 && jobSecretVersionMatches(update.stdout, request, version) {
			mutationSettled = true
			break
		}
		if attempt+1 < jobUpdateAttempts {
			if err := client.wait(ctx, 2*time.Second); err != nil {
				return errCommandFailed
			}
		}
	}
	if !mutationSettled {
		return errCommandFailed
	}
	for attempt := 0; attempt < jobAttestAttempts; attempt++ {
		describe := client.gcloudCommand(ctx, jobAttestTimeout,
			"run", "jobs", "describe", request.Job,
			"--project", request.Project,
			"--region", request.Region,
			"--format=json[no-transforms]",
		)
		if describe.err == nil && describe.exitCode == 0 && jobSecretVersionMatches(describe.stdout, request, version) {
			return nil
		}
		if attempt+1 < jobAttestAttempts {
			if err := client.wait(ctx, 2*time.Second); err != nil {
				return errCommandFailed
			}
		}
	}
	return errCommandFailed
}

// ListSecretVersions returns strict canonical Secret Manager API metadata.
func (client *GCloudClient) ListSecretVersions(ctx context.Context, request Request) ([]SecretVersion, error) {
	if client == nil || client.secretInventory == nil {
		return nil, errCommandFailed
	}
	return listSecretVersions(ctx, client.secretInventory, request)
}

// SecretVersionState describes one exact scoped version.
func (client *GCloudClient) SecretVersionState(ctx context.Context, request Request, version string) (SecretState, error) {
	if !anyVersionPattern.MatchString(version) {
		return "", errCommandFailed
	}
	result := client.gcloudCommand(ctx, secretCommandTimeout,
		"secrets", "versions", "describe", version,
		"--secret="+request.Secret,
		"--project="+request.Project,
		"--format=json[no-transforms](name,state)",
	)
	if result.err != nil || result.exitCode != 0 {
		return "", errCommandFailed
	}
	return parseSecretVersion(result.stdout, request, version, true)
}

// AddSecretVersion returns a syntactically valid identity even when gcloud
// exits non-zero, allowing exact cleanup after an ambiguous mutation.
func (client *GCloudClient) AddSecretVersion(ctx context.Context, request Request, statePath string) (string, error) {
	result := client.gcloudCommand(ctx, secretCommandTimeout,
		"secrets", "versions", "add", request.Secret,
		"--project="+request.Project,
		"--data-file="+statePath,
		"--format=json[no-transforms](name)",
	)
	candidate := parseAddedSecretVersion(result.stdout, request)
	if result.err != nil || result.exitCode != 0 || candidate == "" {
		return candidate, errCommandFailed
	}
	return candidate, nil
}

// DestroySecretVersion destroys and attests one non-placeholder version.
func (client *GCloudClient) DestroySecretVersion(ctx context.Context, request Request, version string) error {
	if !versionPattern.MatchString(version) {
		return errCommandFailed
	}
	state, err := client.SecretVersionState(ctx, request, version)
	if err == nil && state == SecretDestroyed {
		return nil
	}
	_ = client.gcloudCommand(ctx, secretCommandTimeout,
		"secrets", "versions", "destroy", version,
		"--secret="+request.Secret,
		"--project="+request.Project,
		"--quiet",
	)
	for attempt := 0; attempt < destroyAttempts; attempt++ {
		state, stateErr := client.SecretVersionState(ctx, request, version)
		if stateErr == nil && state == SecretDestroyed {
			return nil
		}
		if attempt+1 < destroyAttempts {
			if waitErr := client.wait(ctx, 2*time.Second); waitErr != nil {
				return errCommandFailed
			}
		}
	}
	return errCommandFailed
}

// GenerateSSHKey creates the run-scoped IAP key and fixed known-hosts file.
func (client *GCloudClient) GenerateSSHKey(ctx context.Context, request Request, files LocalFiles) error {
	knownHosts, err := os.OpenFile(files.KnownHosts, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errCommandFailed
	}
	if err := knownHosts.Close(); err != nil {
		return errCommandFailed
	}
	result := client.run.Run(ctx, client.sshKeygen, []string{
		"-q", "-t", "ed25519", "-N", "",
		"-C", "scribe-production-browser-" + request.RunID + "-" + request.RunAttempt,
		"-f", files.SSHKey,
	}, nil, secretCommandTimeout, commandKillGrace, maximumCommandOutput)
	if result.err != nil || result.exitCode != 0 {
		return errCommandFailed
	}
	for _, path := range []string{files.SSHKey, files.SSHKey + ".pub"} {
		if err := os.Chmod(path, 0o600); err != nil {
			return errCommandFailed
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
			return errCommandFailed
		}
	}
	return nil
}

// CreateRemoteStage creates one exact, already-known private VM directory.
func (client *GCloudClient) CreateRemoteStage(ctx context.Context, request Request, files LocalFiles, stage string) error {
	stateDirectory, valid := remoteStateDirectoryForStage(request, stage)
	if !valid {
		return errCommandFailed
	}
	command := "set -eu; umask 077; mkdir -m 0700 -- '" + stage + "'; mkdir -m 0700 -- '" + stateDirectory + "'; test ! -L '" + stage + "'; test ! -L '" + stateDirectory + "'; test \"$(stat -c '%u' -- '" + stage + "')\" = \"$(id -u)\"; test \"$(stat -c '%u' -- '" + stateDirectory + "')\" = \"$(id -u)\"; test \"$(stat -c '%a' -- '" + stage + "')\" = 700; test \"$(stat -c '%a' -- '" + stateDirectory + "')\" = 700"
	result := client.gcloudCommand(ctx, remoteCallTimeout, append(
		[]string{"compute", "ssh"},
		append(client.sshCommon(request, files), "--command="+command)...,
	)...)
	if result.err != nil || result.exitCode != 0 {
		return errCommandFailed
	}
	return nil
}

// CopyTransport copies the private, immutable controller copy to the VM stage.
func (client *GCloudClient) CopyTransport(ctx context.Context, request Request, files LocalFiles, stage string) error {
	if !validRemoteStage(request, stage) {
		return errCommandFailed
	}
	args := []string{"compute", "scp", files.TransportBinary, "cloud-compose@" + request.Instance + ":" + filepath.Join(stage, remoteBinaryName)}
	args = append(args, client.scpCommon(request, files)...)
	result := client.gcloudCommand(ctx, remoteCallTimeout, args...)
	if result.err != nil || result.exitCode != 0 {
		return errCommandFailed
	}
	return nil
}

// InvokeRemote attests and executes the copied typed remote-session command.
func (client *GCloudClient) InvokeRemote(
	ctx context.Context,
	request Request,
	files LocalFiles,
	stage, digest string,
	mode remoteMode,
) error {
	if !validRemoteStage(request, stage) || !remoteDigestPattern.MatchString(digest) || (mode != remotePrepare && mode != remoteMint && mode != remoteCleanup) {
		return errCommandFailed
	}
	remoteBinary := filepath.Join(stage, remoteBinaryName)
	command := remoteInvocationCommand(remoteBinary, mode, request, stage, digest)
	timeout := remotePrepareCallTimeout
	switch mode {
	case remoteMint:
		timeout = remoteMintCallTimeout
	case remoteCleanup:
		timeout = remoteCleanupCallTimeout
	}
	result := client.gcloudCommand(ctx, timeout, append(
		[]string{"compute", "ssh"},
		append(client.sshCommon(request, files), "--command="+command)...,
	)...)
	if result.err != nil || result.exitCode != 0 {
		return remoteInvocationError(mode, result.exitCode)
	}
	return nil
}

// CopyRemoteState copies the mint result to the private controller directory.
func (client *GCloudClient) CopyRemoteState(ctx context.Context, request Request, files LocalFiles, stage string) error {
	stateDirectory, valid := remoteStateDirectoryForStage(request, stage)
	if !valid {
		return errCommandFailed
	}
	_ = os.Remove(files.StorageState)
	args := []string{
		"compute", "scp",
		"cloud-compose@" + request.Instance + ":" + filepath.Join(stateDirectory, "storage-state.json"),
		files.StorageState,
	}
	args = append(args, client.scpCommon(request, files)...)
	result := client.gcloudCommand(ctx, remoteCallTimeout, args...)
	if result.err != nil || result.exitCode != 0 {
		return errCommandFailed
	}
	return nil
}

// CleanupRemote reconciles a known stage. Once typed remote code may have run,
// cleanup must be performed and attested by that same typed boundary.
func (client *GCloudClient) CleanupRemote(
	ctx context.Context,
	request Request,
	files LocalFiles,
	stage, digest string,
	remoteInvoked bool,
) error {
	stateDirectory, valid := remoteStateDirectoryForStage(request, stage)
	if !valid {
		return errCommandFailed
	}
	if remoteInvoked {
		typedCleanupSettled := false
		for attempt := 0; attempt < remoteCleanupAttempts; attempt++ {
			if err := client.InvokeRemote(ctx, request, files, stage, digest, remoteCleanup); err == nil {
				typedCleanupSettled = true
				break
			}
			if client.remoteMaterialAbsent(ctx, request, files, stage, stateDirectory) {
				return nil
			}
			if attempt+1 < remoteCleanupAttempts {
				if err := client.wait(ctx, 2*time.Second); err != nil {
					return errCommandFailed
				}
			}
		}
		if !typedCleanupSettled {
			return errCommandFailed
		}
	}
	for attempt := 0; attempt < remoteCleanupAttempts; attempt++ {
		_ = client.finalizeRemoteStage(ctx, request, files, stage, stateDirectory)
		if client.remoteMaterialAbsent(ctx, request, files, stage, stateDirectory) {
			return nil
		}
		if attempt+1 < remoteCleanupAttempts {
			if err := client.wait(ctx, 2*time.Second); err != nil {
				return errCommandFailed
			}
		}
	}
	return errCommandFailed
}

func (client *GCloudClient) finalizeRemoteStage(ctx context.Context, request Request, files LocalFiles, stage, stateDirectory string) error {
	remoteBinary := filepath.Join(stage, remoteBinaryName)
	stageLock := filepath.Join(stage, ".session.lock")
	state := filepath.Join(stateDirectory, "storage-state.json")
	command := "set -eu; if test -e '" + stateDirectory + "' || test -L '" + stateDirectory + "'; then test -d '" + stateDirectory + "'; test ! -L '" + stateDirectory + "'; test \"$(stat -c '%u' -- '" + stateDirectory + "')\" = \"$(id -u)\"; test \"$(stat -c '%a' -- '" + stateDirectory + "')\" = 700; rm -f -- '" + state + "'; rmdir -- '" + stateDirectory + "'; fi; if test -e '" + stage + "' || test -L '" + stage + "'; then test -d '" + stage + "'; test ! -L '" + stage + "'; test \"$(stat -c '%u' -- '" + stage + "')\" = \"$(id -u)\"; test \"$(stat -c '%a' -- '" + stage + "')\" = 700; rm -f -- '" + remoteBinary + "' '" + stageLock + "'; rmdir -- '" + stage + "'; fi"
	result := client.gcloudCommand(ctx, remoteCallTimeout, append(
		[]string{"compute", "ssh"},
		append(client.sshCommon(request, files), "--command="+command)...,
	)...)
	if result.err != nil || result.exitCode != 0 {
		return errCommandFailed
	}
	return nil
}

// RunReadiness executes the typed Cloud Run lifecycle with exact credential
// version and state digest metadata.
func (client *GCloudClient) RunReadiness(ctx context.Context, request Request, version, digest string) error {
	if !versionPattern.MatchString(version) || !remoteDigestPattern.MatchString(digest) {
		return errCommandFailed
	}
	result := client.run.Run(
		ctx,
		request.CloudReadinessExecutable,
		[]string{request.Job, "browser", request.DiagnosticsPath},
		map[string]string{
			"GCLOUD_PROJECT":                               request.Project,
			"SCRIBE_REGION":                                request.Region,
			"SCRIBE_BROWSER_EXPECTED_SECRET_VERSION":       version,
			"SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256": digest,
		},
		readinessTimeout,
		readinessKillGrace,
		maximumCommandOutput,
	)
	if result.err != nil || result.exitCode != 0 {
		if category := browserTaskFailureCategory(request.DiagnosticsPath); category != "" {
			return browserReadinessFailure{category: category}
		}
		return errCommandFailed
	}
	return nil
}

func (client *GCloudClient) gcloudCommand(ctx context.Context, timeout time.Duration, args ...string) commandResult {
	return client.run.Run(ctx, client.gcloud, args, nil, timeout, commandKillGrace, maximumCommandOutput)
}

func (client *GCloudClient) sshCommon(request Request, files LocalFiles) []string {
	return []string{
		"cloud-compose@" + request.Instance,
		"--project=" + request.Project,
		"--zone=" + request.Zone,
		"--tunnel-through-iap",
		"--ssh-key-file=" + files.SSHKey,
		"--ssh-key-expire-after=50m",
		"--ssh-flag=-o UserKnownHostsFile=" + files.KnownHosts,
		"--ssh-flag=-o GlobalKnownHostsFile=/dev/null",
		"--ssh-flag=-o StrictHostKeyChecking=accept-new",
		"--ssh-flag=-o IdentitiesOnly=yes",
		"--ssh-flag=-o ConnectTimeout=30",
		"--ssh-flag=-o ServerAliveInterval=15",
		"--ssh-flag=-o ServerAliveCountMax=4",
		"--quiet",
	}
}

func (client *GCloudClient) scpCommon(request Request, files LocalFiles) []string {
	return []string{
		"--project=" + request.Project,
		"--zone=" + request.Zone,
		"--tunnel-through-iap",
		"--ssh-key-file=" + files.SSHKey,
		"--ssh-key-expire-after=50m",
		"--scp-flag=-o UserKnownHostsFile=" + files.KnownHosts,
		"--scp-flag=-o GlobalKnownHostsFile=/dev/null",
		"--scp-flag=-o StrictHostKeyChecking=accept-new",
		"--scp-flag=-o IdentitiesOnly=yes",
		"--scp-flag=-o ConnectTimeout=30",
		"--scp-flag=-o ServerAliveInterval=15",
		"--scp-flag=-o ServerAliveCountMax=4",
		"--quiet",
	}
}

func (client *GCloudClient) remoteMaterialAbsent(ctx context.Context, request Request, files LocalFiles, stage, stateDirectory string) bool {
	command := "test ! -e '" + stage + "' && test ! -L '" + stage + "' && test ! -e '" + stateDirectory + "' && test ! -L '" + stateDirectory + "'"
	result := client.gcloudCommand(ctx, secretCommandTimeout, append(
		[]string{"compute", "ssh"},
		append(client.sshCommon(request, files), "--command="+command)...,
	)...)
	return result.err == nil && result.exitCode == 0
}

func remoteInvocationCommand(binary string, mode remoteMode, request Request, stage, digest string) string {
	return "set -eu; chmod 0700 -- '" + binary + "'; actual=$(sha256sum -- '" + binary + "'); actual=${actual%% *}; test \"$actual\" = '" + digest + "'; exec '" + binary + "' remote-session '" + string(mode) + "' '" + request.RunID + "' '" + request.RunAttempt + "' '" + stage + "' '" + digest + "'"
}

func validRemoteStage(request Request, stage string) bool {
	_, valid := remoteStageSuffix(request.RunID, request.RunAttempt, stage)
	return valid
}

type secretVersionWire struct {
	Name  string      `json:"name"`
	State SecretState `json:"state"`
}

func parseSecretVersion(data []byte, request Request, expected string, allowDestroyed bool) (SecretState, error) {
	var record secretVersionWire
	if err := decodeStrictJSON(data, &record); err != nil {
		return "", errCommandFailed
	}
	version, err := parseSecretResource(record.Name, request)
	if err != nil || version != expected {
		return "", errCommandFailed
	}
	if record.State != SecretEnabled && record.State != SecretDisabled && (!allowDestroyed || record.State != SecretDestroyed) {
		return "", errCommandFailed
	}
	return record.State, nil
}

func parseAddedSecretVersion(data []byte, request Request) string {
	var record struct {
		Name string `json:"name"`
	}
	if err := decodeStrictJSON(data, &record); err != nil {
		return ""
	}
	version, err := parseSecretResource(record.Name, request)
	if err != nil || !versionPattern.MatchString(version) {
		return ""
	}
	return version
}

func parseSecretResource(name string, request Request) (string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 6 || parts[0] != "projects" || !validReturnedProject(parts[1], request.Project) || parts[2] != "secrets" || parts[3] != request.Secret || parts[4] != "versions" || !anyVersionPattern.MatchString(parts[5]) {
		return "", errCommandFailed
	}
	return parts[5], nil
}

func validReturnedProject(value, project string) bool {
	return value == project || numericProjectPattern.MatchString(value)
}

func jobSecretVersionMatches(data []byte, request Request, expectedVersion string) bool {
	var root map[string]any
	if err := decodeJSONValue(data, &root); err != nil || root == nil {
		return false
	}
	names, ok := jobRecordNames(root)
	if !ok || len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !jobResourceMatches(name, request) {
			return false
		}
	}
	paths := [][]string{
		{"spec", "template", "spec", "template", "spec", "containers"},
		{"spec", "template", "spec", "template", "containers"},
		{"template", "template", "containers"},
		{"template", "containers"},
	}
	matches := 0
	for _, path := range paths {
		containers, ok := nested(root, path...).([]any)
		if !ok {
			continue
		}
		for _, containerValue := range containers {
			container, ok := containerValue.(map[string]any)
			if !ok {
				return false
			}
			environment, ok := container["env"].([]any)
			if !ok {
				continue
			}
			for _, environmentValue := range environment {
				entry, ok := environmentValue.(map[string]any)
				if !ok {
					return false
				}
				entryName, _ := entry["name"].(string)
				if entryName != browserStateVariable {
					continue
				}
				if !jobSecretReferenceMatches(entry, request, expectedVersion) {
					return false
				}
				matches++
			}
		}
	}
	return matches == 1
}

func jobRecordNames(root map[string]any) ([]string, bool) {
	names := make([]string, 0, 2)
	if metadataValue, exists := root["metadata"]; exists {
		metadata, ok := metadataValue.(map[string]any)
		if !ok {
			return nil, false
		}
		if nameValue, exists := metadata["name"]; exists {
			name, ok := nameValue.(string)
			if !ok || name == "" {
				return nil, false
			}
			names = append(names, name)
		}
	}
	if nameValue, exists := root["name"]; exists {
		name, ok := nameValue.(string)
		if !ok || name == "" {
			return nil, false
		}
		names = append(names, name)
	}
	return names, true
}

func jobSecretReferenceMatches(entry map[string]any, request Request, expectedVersion string) bool {
	if value, exists := entry["value"]; exists {
		text, ok := value.(string)
		if !ok || text != "" {
			return false
		}
	}
	matches := 0
	if valueFrom, ok := entry["valueFrom"].(map[string]any); ok {
		reference, ok := valueFrom["secretKeyRef"].(map[string]any)
		if !ok {
			return false
		}
		name, nameOK := reference["name"].(string)
		version, versionOK := reference["key"].(string)
		if nameOK && versionOK && secretReferenceMatches(name, request) && version == expectedVersion {
			matches++
		} else {
			return false
		}
	} else if _, exists := entry["valueFrom"]; exists {
		return false
	}
	if valueSource, ok := entry["valueSource"].(map[string]any); ok {
		reference, ok := valueSource["secretKeyRef"].(map[string]any)
		if !ok {
			return false
		}
		name, nameOK := reference["secret"].(string)
		version, versionOK := reference["version"].(string)
		if nameOK && versionOK && secretReferenceMatches(name, request) && version == expectedVersion {
			matches++
		} else {
			return false
		}
	} else if _, exists := entry["valueSource"]; exists {
		return false
	}
	return matches == 1
}

func secretReferenceMatches(name string, request Request) bool {
	if name == request.Secret {
		return true
	}
	parts := strings.Split(name, "/")
	return len(parts) == 4 && parts[0] == "projects" && validReturnedProject(parts[1], request.Project) && parts[2] == "secrets" && parts[3] == request.Secret
}

func jobResourceMatches(name string, request Request) bool {
	if name == request.Job {
		return true
	}
	parts := strings.Split(name, "/")
	return len(parts) == 6 && parts[0] == "projects" && validReturnedProject(parts[1], request.Project) && parts[2] == "locations" && parts[3] == request.Region && parts[4] == "jobs" && parts[5] == request.Job
}

func nested(root map[string]any, path ...string) any {
	var current any = root
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[segment]
	}
	return current
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errCommandFailed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errCommandFailed
	}
	return nil
}

func decodeJSONValue(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return errCommandFailed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errCommandFailed
	}
	return nil
}
