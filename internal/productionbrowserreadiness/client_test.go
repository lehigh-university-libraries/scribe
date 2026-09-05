package productionbrowserreadiness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBrowserTaskFailureCategoryAcceptsOnlyReservedTypedStatuses(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		exitCode string
		want     string
	}{
		"legacy manifest":     {exitCode: "35", want: "manifest"},
		"library navigation":  {exitCode: "75", want: "manifest-library-navigation"},
		"second publication":  {exitCode: "89", want: "manifest-second-publication"},
		"upstream request":    {exitCode: "91", want: "manifest-import-upstream-request"},
		"response settlement": {exitCode: "95", want: "manifest-import-response-settlement"},
		"gateway response":    {exitCode: "108", want: "manifest-import-response-http-502"},
		"connect response":    {exitCode: "98", want: "manifest-import-response-connect-deadline-exceeded"},
		"invalid argument":    {exitCode: "112", want: "manifest-import-response-connect-invalid-argument"},
		"other client error":  {exitCode: "124", want: "manifest-import-response-http-other-4xx"},
		"unreserved":          {exitCode: "126", want: ""},
		"unknown":             {exitCode: "unknown", want: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "readiness.log")
			line := "[task] index=0 retried=0 exit_code=" + test.exitCode + " term_signal=0 status_code=10\n"
			if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := browserTaskFailureCategory(path); got != test.want {
				t.Fatalf("browserTaskFailureCategory() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBrowserTaskFailureCategoryRejectsAmbiguousOrUnsafeDiagnostics(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	valid := "[task] index=0 retried=0 exit_code=75 term_signal=0 status_code=10\n"
	duplicate := filepath.Join(directory, "duplicate.log")
	if err := os.WriteFile(duplicate, []byte(valid+valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := browserTaskFailureCategory(duplicate); got != "" {
		t.Fatalf("duplicate task status produced category %q", got)
	}
	target := filepath.Join(directory, "target.log")
	if err := os.WriteFile(target, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got := browserTaskFailureCategory(link); got != "" {
		t.Fatalf("symlink diagnostics produced category %q", got)
	}
}

func TestJobSecretVersionMatchesV1AndV2(t *testing.T) {
	t.Parallel()
	request := Request{
		Project: "scribe-test",
		Region:  "us-east5",
		Job:     "scribe-browser-acde1234",
		Secret:  "scribe-browser-session-acde1234",
	}
	v1 := `{
      "metadata":{"name":"projects/123456789012/locations/us-east5/jobs/scribe-browser-acde1234"},
      "spec":{"template":{"spec":{"template":{"spec":{"containers":[{"env":[{
        "name":"SCRIBE_BROWSER_STORAGE_STATE_JSON",
        "valueFrom":{"secretKeyRef":{"name":"scribe-browser-session-acde1234","key":"42"}}
      }]}]}}}}}
    }`
	v2 := `{
      "name":"projects/123456789012/locations/us-east5/jobs/scribe-browser-acde1234",
      "template":{"template":{"containers":[{"env":[{
        "name":"SCRIBE_BROWSER_STORAGE_STATE_JSON",
        "valueSource":{"secretKeyRef":{"secret":"projects/123456789012/secrets/scribe-browser-session-acde1234","version":"42"}}
      }]}]}}
    }`
	if !jobSecretVersionMatches([]byte(v1), request, "42") {
		t.Fatal("v1 job record did not attest")
	}
	if !jobSecretVersionMatches([]byte(v2), request, "42") {
		t.Fatal("v2 job record did not attest")
	}

	tests := map[string]string{
		"wrong region":      strings.Replace(v2, "us-east5/jobs", "us-west1/jobs", 1),
		"wrong job":         strings.Replace(v2, "scribe-browser-acde1234\"", "scribe-browser-deadbeef\"", 1),
		"wrong secret":      strings.Replace(v2, "session-acde1234", "session-deadbeef", 1),
		"wrong version":     strings.Replace(v2, `"version":"42"`, `"version":"41"`, 1),
		"inline value":      strings.Replace(v2, `"valueSource"`, `"value":"credential","valueSource"`, 1),
		"both references":   strings.Replace(v2, `"valueSource"`, `"valueFrom":{"secretKeyRef":{"name":"scribe-browser-session-acde1234","key":"42"}},"valueSource"`, 1),
		"null":              `null`,
		"conflicting names": strings.Replace(v2, `"name":"projects/123456789012/locations/us-east5/jobs/scribe-browser-acde1234",`, `"name":"projects/123456789012/locations/us-east5/jobs/scribe-browser-acde1234","metadata":{"name":"projects/123456789012/locations/us-west1/jobs/scribe-browser-acde1234"},`, 1),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if jobSecretVersionMatches([]byte(data), request, "42") {
				t.Fatal("jobSecretVersionMatches() accepted invalid record")
			}
		})
	}
	duplicate := strings.Replace(v2, `]}]}}`, `},{"name":"SCRIBE_BROWSER_STORAGE_STATE_JSON","valueSource":{"secretKeyRef":{"secret":"projects/123456789012/secrets/scribe-browser-session-acde1234","version":"42"}}}]}]}}`, 1)
	if jobSecretVersionMatches([]byte(duplicate), request, "42") {
		t.Fatal("jobSecretVersionMatches() accepted duplicate credential references")
	}
}

func TestGCloudReadinessUsesLongSettlementGraceAndExactMetadata(t *testing.T) {
	t.Parallel()
	request := Request{
		Project:                  "scribe-test",
		Region:                   "us-east5",
		Job:                      "scribe-browser-acde1234",
		DiagnosticsPath:          "/artifacts/readiness.log",
		CloudReadinessExecutable: "/tools/cloud-readiness",
	}
	runner := &recordingCommandRunner{results: []commandResult{{}}}
	client := &GCloudClient{run: runner}
	digest := strings.Repeat("a", 64)
	if err := client.RunReadiness(context.Background(), request, "42", digest); err != nil {
		t.Fatalf("RunReadiness() error = %v", err)
	}
	call := runner.calls[0]
	if call.killGrace != 46*time.Minute || call.timeout != 50*time.Minute {
		t.Fatalf("readiness bounds = timeout %s grace %s", call.timeout, call.killGrace)
	}
	wantArgs := []string{request.Job, "browser", request.DiagnosticsPath}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("argv = %#v, want %#v", call.args, wantArgs)
	}
	if call.environment["SCRIBE_BROWSER_EXPECTED_SECRET_VERSION"] != "42" || call.environment["SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256"] != digest {
		t.Fatalf("environment = %#v", call.environment)
	}
}

func TestGCloudReadinessRecoversManifestSubstageFromTypedTaskStatus(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "readiness.log")
	line := "[task] index=0 retried=0 exit_code=82 term_signal=0 status_code=10\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	request := Request{
		Project:                  "scribe-test",
		Region:                   "us-east5",
		Job:                      "scribe-browser-acde1234",
		DiagnosticsPath:          path,
		CloudReadinessExecutable: "/tools/cloud-readiness",
	}
	client := &GCloudClient{run: &recordingCommandRunner{results: []commandResult{{exitCode: 2, err: errCommandFailed}}}}
	err := client.RunReadiness(context.Background(), request, "42", strings.Repeat("a", 64))
	var failure browserReadinessFailure
	if !errors.As(err, &failure) || failure.category != "manifest-first-image" {
		t.Fatalf("RunReadiness() error = %#v", err)
	}
}

func TestGCloudRemoteModeTimeoutsContainVMCommands(t *testing.T) {
	t.Parallel()
	if minimum := 3 * (remoteCommandTimeout + commandKillGrace); remotePrepareCallTimeout <= minimum {
		t.Fatalf("remote prepare call timeout = %s, must contain three VM commands totaling %s", remotePrepareCallTimeout, minimum)
	}
	if minimum := 5 * (remoteCommandTimeout + commandKillGrace); remoteMintCallTimeout <= minimum {
		t.Fatalf("remote mint call timeout = %s, must contain mint and recovery commands totaling %s", remoteMintCallTimeout, minimum)
	}
	if minimum := 2*remoteCleanupTimeout + commandKillGrace; remoteCleanupCallTimeout <= minimum {
		t.Fatalf("remote cleanup call timeout = %s, must contain VM lock and cleanup budgets totaling %s", remoteCleanupCallTimeout, minimum)
	}

	request := Request{
		Project:    "scribe-test",
		Zone:       "us-east5-c",
		Instance:   "scribe",
		RunID:      "76543210",
		RunAttempt: "3",
	}
	stage := remoteStagePrefix(request.RunID, request.RunAttempt) + "AbCdEf1234"
	runner := &recordingCommandRunner{results: []commandResult{{}, {}, {}}}
	client := &GCloudClient{gcloud: "/tools/gcloud", run: runner}
	files := LocalFiles{SSHKey: "/private/id", KnownHosts: "/private/known-hosts"}
	cases := []struct {
		mode    remoteMode
		timeout time.Duration
	}{
		{mode: remotePrepare, timeout: remotePrepareCallTimeout},
		{mode: remoteMint, timeout: remoteMintCallTimeout},
		{mode: remoteCleanup, timeout: remoteCleanupCallTimeout},
	}
	for _, test := range cases {
		if err := client.InvokeRemote(context.Background(), request, files, stage, strings.Repeat("a", 64), test.mode); err != nil {
			t.Fatalf("InvokeRemote(%s) error = %v", test.mode, err)
		}
	}
	for index, test := range cases {
		if runner.calls[index].timeout != test.timeout {
			t.Fatalf("InvokeRemote(%s) timeout = %s, want %s", test.mode, runner.calls[index].timeout, test.timeout)
		}
	}
}

func TestGCloudRemoteMintMapsOnlyReservedVMStatuses(t *testing.T) {
	t.Parallel()
	request := Request{
		Project:    "scribe-test",
		Zone:       "us-east5-c",
		Instance:   "scribe",
		RunID:      "76543210",
		RunAttempt: "3",
	}
	stage := remoteStagePrefix(request.RunID, request.RunAttempt) + "AbCdEf1234"
	files := LocalFiles{SSHKey: "/private/id", KnownHosts: "/private/known-hosts"}
	digest := strings.Repeat("a", 64)
	secretOutput := []byte("storage-state={\"cookie\":\"super-secret\"}")
	tests := []struct {
		name     string
		exitCode int
		want     error
	}{
		{name: "session command", exitCode: remoteMintSessionCommandExitCode, want: errRemoteMintSessionCommand},
		{name: "state export", exitCode: remoteMintStateExportExitCode, want: errRemoteMintStateExport},
		{name: "container cleanup", exitCode: remoteMintContainerCleanupExitCode, want: errRemoteMintContainerCleanup},
		{name: "host state contract", exitCode: remoteMintHostStateContractExitCode, want: errRemoteMintHostStateContract},
		{name: "recovery cleanup", exitCode: remoteMintRecoveryCleanupExitCode, want: errRemoteMintRecoveryCleanup},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingCommandRunner{results: []commandResult{{stdout: secretOutput, exitCode: test.exitCode, err: errCommandFailed}}}
			client := &GCloudClient{gcloud: "/tools/gcloud", run: runner}
			err := client.InvokeRemote(context.Background(), request, files, stage, digest, remoteMint)
			if !errors.Is(err, test.want) {
				t.Fatalf("InvokeRemote() error = %v, want sentinel %v", err, test.want)
			}
			if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), string(secretOutput)) {
				t.Fatalf("InvokeRemote() leaked child output: %q", err)
			}
		})
	}

	for _, test := range []struct {
		name     string
		mode     remoteMode
		exitCode int
	}{
		{name: "mint generic", mode: remoteMint, exitCode: 1},
		{name: "prepare reserved-looking", mode: remotePrepare, exitCode: remoteMintSessionCommandExitCode},
		{name: "cleanup reserved-looking", mode: remoteCleanup, exitCode: remoteMintRecoveryCleanupExitCode},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingCommandRunner{results: []commandResult{{stdout: secretOutput, exitCode: test.exitCode, err: errCommandFailed}}}
			client := &GCloudClient{gcloud: "/tools/gcloud", run: runner}
			err := client.InvokeRemote(context.Background(), request, files, stage, digest, test.mode)
			if err != errCommandFailed {
				t.Fatalf("InvokeRemote() error = %v, want generic command failure", err)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("InvokeRemote() leaked child output: %q", err)
			}
		})
	}
}

func TestGCloudRemoteStageSeparatesExecutableAndCredentialState(t *testing.T) {
	t.Parallel()
	request := Request{
		Project:    "scribe-test",
		Zone:       "us-east5-c",
		Instance:   "scribe",
		RunID:      "76543210",
		RunAttempt: "3",
	}
	stage := remoteStagePrefix(request.RunID, request.RunAttempt) + "AbCdEf1234"
	stateDirectory, valid := remoteStateDirectoryForStage(request, stage)
	if !valid {
		t.Fatal("valid remote stage was rejected")
	}
	runner := &recordingCommandRunner{results: []commandResult{{}, {}}}
	client := &GCloudClient{gcloud: "/tools/gcloud", run: runner}
	files := LocalFiles{
		SSHKey:       "/private/id",
		KnownHosts:   "/private/known-hosts",
		StorageState: "/private/storage-state.json",
	}
	if err := client.CreateRemoteStage(context.Background(), request, files, stage); err != nil {
		t.Fatalf("CreateRemoteStage() error = %v", err)
	}
	if err := client.CopyRemoteState(context.Background(), request, files, stage); err != nil {
		t.Fatalf("CopyRemoteState() error = %v", err)
	}
	createCommand := strings.Join(runner.calls[0].args, " ")
	for _, directory := range []string{stage, stateDirectory} {
		if !strings.Contains(createCommand, "mkdir -m 0700 -- '"+directory+"'") {
			t.Fatalf("private directory %q missing from create command: %s", directory, createCommand)
		}
	}
	wantSource := "cloud-compose@" + request.Instance + ":" + filepath.Join(stateDirectory, "storage-state.json")
	if len(runner.calls[1].args) < 3 || runner.calls[1].args[2] != wantSource {
		t.Fatalf("state copy source = %#v, want %q", runner.calls[1].args, wantSource)
	}
}

func TestGCloudRemoteCleanupContainsVMDeadlineAndRetriesOnlyFinalization(t *testing.T) {
	t.Parallel()
	typedCleanupBudget := time.Duration(remoteCleanupAttempts)*(remoteCleanupCallTimeout+secretCommandTimeout+2*commandKillGrace) +
		time.Duration(remoteCleanupAttempts-1)*2*time.Second
	finalizationBudget := time.Duration(remoteCleanupAttempts)*(remoteCallTimeout+secretCommandTimeout+2*commandKillGrace) +
		time.Duration(remoteCleanupAttempts-1)*2*time.Second
	jobRestoreBudget := time.Duration(jobUpdateAttempts)*(jobUpdateTimeout+commandKillGrace) +
		time.Duration(jobAttestAttempts)*(jobAttestTimeout+commandKillGrace) +
		time.Duration(jobUpdateAttempts+jobAttestAttempts-2)*2*time.Second
	combinedBudget := typedCleanupBudget + finalizationBudget + jobRestoreBudget
	if cleanupTimeout <= combinedBudget {
		t.Fatalf("controller cleanup timeout = %s, must contain remote cleanup, finalization, and job restoration budget %s", cleanupTimeout, combinedBudget)
	}

	request := Request{
		Project:    "scribe-test",
		Zone:       "us-east5-c",
		Instance:   "scribe",
		RunID:      "76543210",
		RunAttempt: "3",
	}
	stage := remoteStagePrefix(request.RunID, request.RunAttempt) + "AbCdEf1234"
	runner := &recordingCommandRunner{results: []commandResult{
		{},
		{exitCode: 124, err: errCommandFailed},
		{exitCode: 1, err: errCommandFailed},
		{},
		{},
	}}
	client := &GCloudClient{
		gcloud: "/tools/gcloud",
		run:    runner,
		wait:   func(context.Context, time.Duration) error { return nil },
	}
	files := LocalFiles{SSHKey: "/private/id", KnownHosts: "/private/known-hosts"}
	if err := client.CleanupRemote(context.Background(), request, files, stage, strings.Repeat("a", 64), true); err != nil {
		t.Fatalf("CleanupRemote() error = %v", err)
	}
	remoteCleanupCalls := 0
	finalizationCalls := 0
	for _, call := range runner.calls {
		command := strings.Join(call.args, " ")
		if strings.Contains(command, "remote-session 'cleanup'") {
			remoteCleanupCalls++
			if call.timeout != remoteCleanupCallTimeout {
				t.Fatalf("typed cleanup timeout = %s", call.timeout)
			}
		}
		if strings.Contains(command, "rmdir -- '"+stage+"'") {
			finalizationCalls++
		}
	}
	if remoteCleanupCalls != 1 || finalizationCalls != 2 {
		t.Fatalf("cleanup calls = typed %d, finalization %d; calls = %#v", remoteCleanupCalls, finalizationCalls, runner.calls)
	}
}

func TestGCloudSetJobSecretVersionNeverTrustsStaleDescribeAfterAmbiguousUpdate(t *testing.T) {
	t.Parallel()
	request := Request{Project: "scribe-test", Region: "us-east5", Job: "scribe-browser-acde1234", Secret: "scribe-browser-session-acde1234"}
	runner := &recordingCommandRunner{results: []commandResult{
		{exitCode: 124, err: errCommandFailed},
	}}
	client := &GCloudClient{gcloud: "/tools/gcloud", run: runner, wait: func(context.Context, time.Duration) error { return context.Canceled }}
	if err := client.SetJobSecretVersion(context.Background(), request, "42"); err == nil {
		t.Fatal("SetJobSecretVersion() accepted an ambiguous update from stale expected state")
	}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args[:4], []string{"run", "jobs", "update", request.Job}) {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestGCloudSetJobSecretVersionRetriesDesiredMutationBeforeDescribe(t *testing.T) {
	t.Parallel()
	request := Request{Project: "scribe-test", Region: "us-east5", Job: "scribe-browser-acde1234", Secret: "scribe-browser-session-acde1234"}
	matching := []byte(`{
      "name":"projects/123456789012/locations/us-east5/jobs/scribe-browser-acde1234",
      "template":{"containers":[{"env":[{"name":"SCRIBE_BROWSER_STORAGE_STATE_JSON","valueSource":{"secretKeyRef":{"secret":"projects/123456789012/secrets/scribe-browser-session-acde1234","version":"42"}}}]}]}
    }`)
	runner := &recordingCommandRunner{results: []commandResult{
		{exitCode: 124, err: errCommandFailed},
		{stdout: matching},
		{stdout: matching},
	}}
	client := &GCloudClient{gcloud: "/tools/gcloud", run: runner, wait: func(context.Context, time.Duration) error { return nil }}
	if err := client.SetJobSecretVersion(context.Background(), request, "42"); err != nil {
		t.Fatalf("SetJobSecretVersion() error = %v", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %#v", runner.calls)
	}
	for index := 0; index < 2; index++ {
		if !reflect.DeepEqual(runner.calls[index].args[:4], []string{"run", "jobs", "update", request.Job}) {
			t.Fatalf("call %d = %#v, want desired update", index, runner.calls[index])
		}
	}
	if !reflect.DeepEqual(runner.calls[2].args[:4], []string{"run", "jobs", "describe", request.Job}) {
		t.Fatalf("call 2 = %#v, want independent describe", runner.calls[2])
	}
}

func TestGCloudAddSecretVersionPreservesExactIdentityOnAmbiguousExit(t *testing.T) {
	t.Parallel()
	request := Request{Project: "scribe-test", Secret: "scribe-browser-session-acde1234"}
	runner := &recordingCommandRunner{results: []commandResult{{
		stdout:   []byte(`{"name":"projects/123456789012/secrets/scribe-browser-session-acde1234/versions/42"}`),
		exitCode: 124,
		err:      errCommandFailed,
	}}}
	client := &GCloudClient{gcloud: "/tools/gcloud", run: runner}
	version, err := client.AddSecretVersion(context.Background(), request, "/private/storage-state.json")
	if version != "42" || err == nil {
		t.Fatalf("AddSecretVersion() = %q, %v", version, err)
	}
	want := []string{
		"secrets", "versions", "add", request.Secret,
		"--project=scribe-test",
		"--data-file=/private/storage-state.json",
		"--format=json[no-transforms](name)",
	}
	if !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("argv = %#v, want %#v", runner.calls[0].args, want)
	}

	for name, output := range map[string]string{
		"other secret":  `{"name":"projects/123456789012/secrets/other/versions/42"}`,
		"other project": `{"name":"projects/other-project/secrets/scribe-browser-session-acde1234/versions/42"}`,
		"version one":   `{"name":"projects/123456789012/secrets/scribe-browser-session-acde1234/versions/1"}`,
		"array":         `[{"name":"projects/123456789012/secrets/scribe-browser-session-acde1234/versions/42"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if candidate := parseAddedSecretVersion([]byte(output), request); candidate != "" {
				t.Fatalf("parseAddedSecretVersion() = %q", candidate)
			}
		})
	}
}

type recordedCommand struct {
	executable  string
	args        []string
	environment map[string]string
	timeout     time.Duration
	killGrace   time.Duration
	outputLimit int
}

type recordingCommandRunner struct {
	calls   []recordedCommand
	results []commandResult
}

func (runner *recordingCommandRunner) Run(
	_ context.Context,
	executable string,
	args []string,
	environment map[string]string,
	timeout, killGrace time.Duration,
	outputLimit int,
) commandResult {
	call := recordedCommand{
		executable:  executable,
		args:        append([]string(nil), args...),
		environment: cloneStrings(environment),
		timeout:     timeout,
		killGrace:   killGrace,
		outputLimit: outputLimit,
	}
	runner.calls = append(runner.calls, call)
	if len(runner.results) == 0 {
		return commandResult{exitCode: 125, err: errors.New("missing fake command result")}
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	return result
}

func cloneStrings(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
