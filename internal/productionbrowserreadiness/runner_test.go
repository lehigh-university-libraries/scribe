package productionbrowserreadiness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunnerCompletesExactCredentialLifecycle(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	now := time.Unix(2_000_000_000, 0)
	client := newFakeTransportClient(now)
	client.versions["2"] = SecretEnabled
	client.versions["40"] = SecretDisabled
	runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
	var stdout, stderr bytes.Buffer
	result := runner.Run(context.Background(), request, &stdout, &stderr)
	if result.ExitCode != ExitSuccess || result.Category != "" {
		t.Fatalf("Run() = %+v; stderr=%q", result, stderr.String())
	}
	if !strings.Contains(stdout.String(), request.Job) || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	want := []string{
		"preflight",
		"job:1",
		"versions",
		"destroy:2",
		"destroy:40",
		"versions",
		"ssh-key",
		"remote-stage",
		"transport-copy",
		"remote:prepare",
		"remote:mint",
		"state-copy",
		"remote-cleanup:true",
		"add:42",
		"state:42",
		"job:42",
		"readiness:42",
		"job:1",
		"destroy:42",
		"versions",
		"versions",
	}
	if !equalStrings(client.events, want) {
		t.Fatalf("events:\n got %#v\nwant %#v", client.events, want)
	}
	if len(client.versions) != 1 || client.versions["1"] != SecretEnabled || client.jobVersion != "1" {
		t.Fatalf("final secret/job state = %#v, %q", client.versions, client.jobVersion)
	}
	entries, err := os.ReadDir(request.TemporaryRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "scribe-production-browser.") {
			t.Fatalf("private temporary directory retained: %s", entry.Name())
		}
	}
}

func TestRunnerReportsTypedBrowserTaskFailure(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	now := time.Unix(2_000_000_000, 0)
	client := newFakeTransportClient(now)
	client.readiness = func() error {
		return browserReadinessFailure{category: "manifest-first-image"}
	}
	runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
	var stderr bytes.Buffer
	result := runner.Run(context.Background(), request, nil, &stderr)
	if result.ExitCode != ExitInvalidInvocation || result.Category != "browser-execution-manifest-first-image" {
		t.Fatalf("Run() = %+v; stderr=%q", result, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Production browser readiness transport failed: browser-execution-manifest-first-image.") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunnerPreflightFailureHasNoCloudOrRemoteMutation(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	now := time.Unix(2_000_000_000, 0)
	client := newFakeTransportClient(now)
	client.preflightError = true
	runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
	result := runner.Run(context.Background(), request, nil, nil)
	if result.ExitCode != ExitInvalidInvocation || result.Category != "browser-preflight" {
		t.Fatalf("Run() = %+v", result)
	}
	if !equalStrings(client.events, []string{"preflight"}) {
		t.Fatalf("events = %#v, want preflight only", client.events)
	}
}

func TestRunnerReconcileRequiresExactPlaceholderInventory(t *testing.T) {
	t.Parallel()
	overBound := make([]SecretVersion, maximumSecretVersions+1)
	for index := range overBound {
		overBound[index] = SecretVersion{Version: strconv.Itoa(index + 1), State: SecretEnabled}
	}
	tests := map[string]secretInventoryObservation{
		"duplicate v1": {versions: []SecretVersion{
			{Version: "1", State: SecretEnabled},
			{Version: "1", State: SecretEnabled},
		}},
		"over bound":          {versions: overBound},
		"invalid record":      {versions: []SecretVersion{{Version: "0", State: SecretEnabled}}},
		"unexpected v1 state": {versions: []SecretVersion{{Version: "1", State: SecretDestroyed}}},
		"invalid client data": {err: errCommandFailed},
	}
	for name, observation := range tests {
		name, observation := name, observation
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := newFakeTransportClient(time.Unix(2_000_000_000, 0))
			client.inventoryQueue = []secretInventoryObservation{observation}
			waits := 0
			runner := NewRunner(client, WithWait(func(context.Context, time.Duration) error {
				waits++
				return nil
			}))
			request := Request{Project: "scribe-test", Secret: "scribe-browser-session-acde1234"}
			if err := runner.reconcileSecretVersions(context.Background(), request); err == nil {
				t.Fatal("reconcileSecretVersions() accepted invalid inventory")
			}
			if waits != 0 {
				t.Fatalf("reconcileSecretVersions() waited %d times for invalid inventory", waits)
			}
		})
	}
}

func TestRunnerReconcilePollsEventuallyConsistentInventory(t *testing.T) {
	t.Parallel()
	client := newFakeTransportClient(time.Unix(2_000_000_000, 0))
	client.inventoryQueue = []secretInventoryObservation{
		{err: errSecretInventoryUnavailable},
		{versions: []SecretVersion{}},
		{versions: []SecretVersion{{Version: "2", State: SecretEnabled}}},
		{versions: []SecretVersion{{Version: "1", State: SecretDisabled}}},
		{versions: []SecretVersion{{Version: "1", State: SecretEnabled}, {Version: "2", State: SecretEnabled}}},
		{versions: []SecretVersion{{Version: "1", State: SecretEnabled}, {Version: "2", State: SecretEnabled}}},
		{versions: []SecretVersion{{Version: "1", State: SecretEnabled}, {Version: "2", State: SecretEnabled}}},
		{versions: []SecretVersion{{Version: "1", State: SecretEnabled}}},
	}
	waits := 0
	runner := NewRunner(client, WithWait(func(context.Context, time.Duration) error {
		waits++
		return nil
	}))
	request := Request{Project: "scribe-test", Secret: "scribe-browser-session-acde1234"}
	if err := runner.reconcileSecretVersions(context.Background(), request); err != nil {
		t.Fatalf("reconcileSecretVersions() = %v", err)
	}
	if waits != 5 {
		t.Fatalf("settlement waits = %d, want 5", waits)
	}
	wantEvents := []string{
		"versions",
		"versions",
		"versions",
		"versions",
		"versions", "destroy:2", "versions",
		"versions", "destroy:2", "versions",
	}
	if !equalStrings(client.events, wantEvents) {
		t.Fatalf("events:\n got %#v\nwant %#v", client.events, wantEvents)
	}
}

func TestRunnerReconcileReturnsBoundedUnsettledCategory(t *testing.T) {
	t.Parallel()
	client := newFakeTransportClient(time.Unix(2_000_000_000, 0))
	client.inventoryQueue = make([]secretInventoryObservation, secretSettlementAttempts)
	for index := range client.inventoryQueue {
		client.inventoryQueue[index].err = errSecretInventoryUnavailable
	}
	waits := 0
	runner := NewRunner(client, WithWait(func(context.Context, time.Duration) error {
		waits++
		return nil
	}))
	request := Request{Project: "scribe-test", Secret: "scribe-browser-session-acde1234"}
	err := runner.reconcileSecretVersions(context.Background(), request)
	if !errors.Is(err, errSecretInventoryUnavailable) {
		t.Fatalf("reconcileSecretVersions() error = %v", err)
	}
	if waits != secretSettlementAttempts-1 {
		t.Fatalf("settlement waits = %d, want %d", waits, secretSettlementAttempts-1)
	}
	if category := secretReconciliationCategory(context.Background(), "stale-secret-cleanup", err); category != "stale-secret-cleanup-inventory-unavailable" {
		t.Fatalf("category = %q", category)
	}
}

func TestRunnerCleansAmbiguousSecretAdd(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	now := time.Unix(2_000_000_000, 0)
	client := newFakeTransportClient(now)
	client.addError = true
	runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
	var stderr bytes.Buffer
	result := runner.Run(context.Background(), request, nil, &stderr)
	if result.ExitCode != ExitInvalidInvocation || result.Category != "secret-version-create" {
		t.Fatalf("Run() = %+v; stderr=%q", result, stderr.String())
	}
	if _, exists := client.versions["42"]; exists {
		t.Fatal("ambiguous server-created secret version was retained")
	}
	assertEventOrder(t, client.events, "add:42", "job:1", "destroy:42", "versions")
	if strings.Contains(stderr.String(), request.Secret) || strings.Contains(stderr.String(), request.TemporaryRoot) {
		t.Fatalf("stderr leaked sensitive resource/path: %q", stderr.String())
	}
}

func TestRunnerWaitsForReadinessSettlementBeforeSignalCleanup(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	now := time.Unix(2_000_000_000, 0)
	client := newFakeTransportClient(now)
	ctx, cancel := context.WithCancelCause(context.Background())
	client.readiness = func() error {
		client.events = append(client.events, "execution-settled")
		cancel(SignalCause(ExitTerminated))
		return errCommandFailed
	}
	runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
	result := runner.Run(ctx, request, nil, nil)
	if result.ExitCode != ExitTerminated || result.Category != "interrupted" {
		t.Fatalf("Run() = %+v", result)
	}
	assertEventOrder(t, client.events, "readiness:42", "execution-settled", "job:1", "destroy:42")
	if client.jobVersion != "1" {
		t.Fatalf("job version = %q after signal", client.jobVersion)
	}
}

func TestRunnerCleanupFailureOverridesSignal(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	now := time.Unix(2_000_000_000, 0)
	client := newFakeTransportClient(now)
	ctx, cancel := context.WithCancelCause(context.Background())
	client.readiness = func() error {
		cancel(SignalCause(ExitTerminated))
		client.failRestore = true
		return errCommandFailed
	}
	runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
	var stderr bytes.Buffer
	result := runner.Run(ctx, request, nil, &stderr)
	if result.ExitCode != ExitInvalidInvocation || result.Category != "cleanup-unconfirmed" {
		t.Fatalf("Run() = %+v; stderr=%q", result, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cleanup failed: job-secret-restore") {
		t.Fatalf("cleanup category missing: %q", stderr.String())
	}
}

func TestRunnerCleanupFailurePreservesRedactedPrimaryCategory(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	now := time.Unix(2_000_000_000, 0)
	client := newFakeTransportClient(now)
	client.failRemoteMode = remotePrepare
	client.failRemoteCleanup = true
	runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
	var stderr bytes.Buffer
	result := runner.Run(context.Background(), request, nil, &stderr)
	if result.ExitCode != ExitInvalidInvocation || result.Category != "cleanup-unconfirmed" {
		t.Fatalf("Run() = %+v; stderr=%q", result, stderr.String())
	}
	output := stderr.String()
	for _, required := range []string{
		"Production browser readiness primary failure: remote-preclean.",
		"Production browser readiness cleanup failed: remote-state.",
		"Production browser readiness transport failed: cleanup-unconfirmed.",
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("stderr missing %q: %q", required, output)
		}
	}
	for _, forbidden := range []string{
		request.Project,
		request.RunID,
		request.Job,
		request.Secret,
		request.DiagnosticsPath,
		request.TemporaryRoot,
	} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("stderr leaked request value %q: %q", forbidden, output)
		}
	}
}

func TestRunnerRemoteFailureUsesTypedCleanup(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	now := time.Unix(2_000_000_000, 0)
	client := newFakeTransportClient(now)
	client.failRemoteMode = remoteMint
	runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
	result := runner.Run(context.Background(), request, nil, nil)
	if result.ExitCode != ExitInvalidInvocation || result.Category != "remote-mint" {
		t.Fatalf("Run() = %+v", result)
	}
	assertEventOrder(t, client.events, "remote:mint", "remote-cleanup:true", "job:1")
}

func TestRunnerMapsRemoteMintSentinelsWithoutLeakingErrorDetails(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		category string
	}{
		{name: "session command", err: errRemoteMintSessionCommand, category: "remote-mint-session-command"},
		{name: "state export", err: errRemoteMintStateExport, category: "remote-mint-state-export"},
		{name: "container cleanup", err: errRemoteMintContainerCleanup, category: "remote-mint-container-cleanup"},
		{name: "host state contract", err: errRemoteMintHostStateContract, category: "remote-mint-host-state-contract"},
		{name: "recovery cleanup", err: errRemoteMintRecoveryCleanup, category: "remote-mint-recovery-cleanup"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := validTestRequest(t)
			now := time.Unix(2_000_000_000, 0)
			client := newFakeTransportClient(now)
			client.failRemoteMode = remoteMint
			client.remoteError = fmt.Errorf("arbitrary child output session-cookie=super-secret: %w", test.err)
			runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
			var stderr bytes.Buffer
			result := runner.Run(context.Background(), request, nil, &stderr)
			if result.ExitCode != ExitInvalidInvocation || result.Category != test.category {
				t.Fatalf("Run() = %+v; stderr=%q", result, stderr.String())
			}
			if strings.Contains(stderr.String(), "super-secret") || strings.Contains(stderr.String(), "session-cookie") {
				t.Fatalf("runner leaked remote error details: %q", stderr.String())
			}
			assertEventOrder(t, client.events, "remote:mint", "remote-cleanup:true", "job:1")
		})
	}
}

func TestRunnerCleanupFailureOverridesRemoteMintSubstage(t *testing.T) {
	t.Parallel()
	request := validTestRequest(t)
	now := time.Unix(2_000_000_000, 0)
	client := newFakeTransportClient(now)
	client.failRemoteMode = remoteMint
	client.remoteError = fmt.Errorf("storage-state=super-secret: %w", errRemoteMintStateExport)
	client.failRemoteCleanup = true
	runner := NewRunner(client, WithClock(func() time.Time { return now }), WithRandomSource(bytes.NewReader(make([]byte, 10))))
	var stderr bytes.Buffer
	result := runner.Run(context.Background(), request, nil, &stderr)
	if result.ExitCode != ExitInvalidInvocation || result.Category != "cleanup-unconfirmed" {
		t.Fatalf("Run() = %+v; stderr=%q", result, stderr.String())
	}
	want := "Production browser readiness primary failure: remote-mint-state-export."
	if !strings.Contains(stderr.String(), want) || strings.Contains(stderr.String(), "super-secret") {
		t.Fatalf("runner did not preserve a redacted primary category: %q", stderr.String())
	}
	assertEventOrder(t, client.events, "remote:mint", "remote-cleanup:true", "job:1")
}

func TestValidateStorageStateRejectsMalformedMaterial(t *testing.T) {
	t.Parallel()
	now := time.Unix(2_000_000_000, 0)
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.json")
	writeStorageState(t, valid, now.Add(45*time.Minute), "scribe-123456789.us-east5.run.app")
	state, digest, err := validateStorageState(valid, now)
	if err != nil || state.cookieExpiry == 0 || len(digest) != 64 {
		t.Fatalf("validateStorageState(valid) = %+v, %q, %v", state, digest, err)
	}

	tests := map[string]func(string){
		"mode": func(path string) {
			writeStorageState(t, path, now.Add(45*time.Minute), "scribe.example")
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"short expiry": func(path string) { writeStorageState(t, path, now.Add(40*time.Minute), "scribe.example") },
		"long expiry":  func(path string) { writeStorageState(t, path, now.Add(51*time.Minute), "scribe.example") },
		"domain":       func(path string) { writeStorageState(t, path, now.Add(45*time.Minute), "bad domain") },
		"unknown key": func(path string) {
			writeStorageState(t, path, now.Add(45*time.Minute), "scribe.example")
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			data = bytes.Replace(data, []byte(`"origins":[]`), []byte(`"origins":[],"credential":"leak"`), 1)
			if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, strings.ReplaceAll(name, " ", "-")+".json")
			prepare(path)
			if _, _, err := validateStorageState(path, now); err == nil {
				t.Fatal("validateStorageState() accepted malformed material")
			}
		})
	}
}

type fakeTransportClient struct {
	now               time.Time
	events            []string
	versions          map[string]SecretState
	inventoryQueue    []secretInventoryObservation
	jobVersion        string
	preflightError    bool
	addError          bool
	failRestore       bool
	failRemoteCleanup bool
	failRemoteMode    remoteMode
	remoteError       error
	readiness         func() error
}

type secretInventoryObservation struct {
	versions []SecretVersion
	err      error
}

func newFakeTransportClient(now time.Time) *fakeTransportClient {
	return &fakeTransportClient{now: now, versions: map[string]SecretState{"1": SecretEnabled}, jobVersion: "99"}
}

func (client *fakeTransportClient) Preflight(context.Context, Request) error {
	client.events = append(client.events, "preflight")
	if client.preflightError {
		return errCommandFailed
	}
	return nil
}

func (client *fakeTransportClient) SetJobSecretVersion(_ context.Context, _ Request, version string) error {
	client.events = append(client.events, "job:"+version)
	if version == "1" && client.failRestore {
		return errCommandFailed
	}
	client.jobVersion = version
	return nil
}

func (client *fakeTransportClient) ListSecretVersions(context.Context, Request) ([]SecretVersion, error) {
	client.events = append(client.events, "versions")
	if len(client.inventoryQueue) > 0 {
		observation := client.inventoryQueue[0]
		client.inventoryQueue = client.inventoryQueue[1:]
		return append([]SecretVersion(nil), observation.versions...), observation.err
	}
	numbers := make([]int, 0, len(client.versions))
	for version, state := range client.versions {
		if state == SecretDestroyed {
			continue
		}
		number, _ := strconv.Atoi(version)
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	versions := make([]SecretVersion, 0, len(numbers))
	for _, number := range numbers {
		version := strconv.Itoa(number)
		versions = append(versions, SecretVersion{Version: version, State: client.versions[version]})
	}
	return versions, nil
}

func (client *fakeTransportClient) SecretVersionState(_ context.Context, _ Request, version string) (SecretState, error) {
	client.events = append(client.events, "state:"+version)
	state, ok := client.versions[version]
	if !ok {
		return "", errCommandFailed
	}
	return state, nil
}

func (client *fakeTransportClient) AddSecretVersion(context.Context, Request, string) (string, error) {
	client.events = append(client.events, "add:42")
	client.versions["42"] = SecretEnabled
	if client.addError {
		return "42", errCommandFailed
	}
	return "42", nil
}

func (client *fakeTransportClient) DestroySecretVersion(_ context.Context, _ Request, version string) error {
	client.events = append(client.events, "destroy:"+version)
	delete(client.versions, version)
	return nil
}

func (client *fakeTransportClient) GenerateSSHKey(context.Context, Request, LocalFiles) error {
	client.events = append(client.events, "ssh-key")
	return nil
}

func (client *fakeTransportClient) CreateRemoteStage(context.Context, Request, LocalFiles, string) error {
	client.events = append(client.events, "remote-stage")
	return nil
}

func (client *fakeTransportClient) CopyTransport(context.Context, Request, LocalFiles, string) error {
	client.events = append(client.events, "transport-copy")
	return nil
}

func (client *fakeTransportClient) InvokeRemote(_ context.Context, _ Request, _ LocalFiles, _ string, _ string, mode remoteMode) error {
	client.events = append(client.events, "remote:"+string(mode))
	if mode == client.failRemoteMode {
		if client.remoteError != nil {
			return client.remoteError
		}
		return errCommandFailed
	}
	return nil
}

func (client *fakeTransportClient) CopyRemoteState(_ context.Context, _ Request, files LocalFiles, _ string) error {
	client.events = append(client.events, "state-copy")
	return writeStorageStateFile(files.StorageState, client.now.Add(45*time.Minute), "scribe-123456789.us-east5.run.app")
}

func (client *fakeTransportClient) CleanupRemote(_ context.Context, _ Request, _ LocalFiles, _ string, _ string, invoked bool) error {
	client.events = append(client.events, fmt.Sprintf("remote-cleanup:%t", invoked))
	if client.failRemoteCleanup {
		return errCommandFailed
	}
	return nil
}

func (client *fakeTransportClient) RunReadiness(_ context.Context, _ Request, version, _ string) error {
	client.events = append(client.events, "readiness:"+version)
	if client.readiness != nil {
		return client.readiness()
	}
	return nil
}

func writeStorageState(t *testing.T, path string, expiry time.Time, domain string) {
	t.Helper()
	if err := writeStorageStateFile(path, expiry, domain); err != nil {
		t.Fatal(err)
	}
}

func writeStorageStateFile(path string, expiry time.Time, domain string) error {
	state := map[string]any{
		"cookies": []map[string]any{{
			"name":     "scribe_session",
			"value":    strings.Repeat("A", 64),
			"domain":   domain,
			"path":     "/",
			"expires":  expiry.Unix(),
			"httpOnly": true,
			"secure":   true,
			"sameSite": "Lax",
		}},
		"origins": []any{},
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) < minimumStateBytes {
		return errors.New("test storage state unexpectedly short")
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func assertEventOrder(t *testing.T, events []string, expected ...string) {
	t.Helper()
	position := -1
	for _, wanted := range expected {
		found := -1
		for index := position + 1; index < len(events); index++ {
			if events[index] == wanted {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("event %q missing after index %d in %#v", wanted, position, events)
		}
		position = found
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
