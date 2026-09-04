package cloudrunreadiness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultCommandTimeout = 30 * time.Second
	defaultKillGrace      = 5 * time.Second
	maximumCommandOutput  = 8 << 20
	maximumLogEntries     = 100
	maximumLogMarkers     = 32
)

var (
	errControlPlane = errors.New("cloud run control plane operation failed")
	errOutputLimit  = errors.New("cloud run control plane response exceeded its limit")
)

// LaunchResult preserves the bounded execution identity and process exit code
// even when gcloud reports a non-zero status.
type LaunchResult struct {
	Candidate string
	ExitCode  int
}

// Client is the typed Cloud Run surface used by Runner. Implementations must
// discard arbitrary stderr and return only normalized records.
type Client interface {
	ListExecutions(ctx context.Context, job string, maximum int) ([]Execution, error)
	DescribeExecution(ctx context.Context, job, execution string) (Execution, error)
	Execute(ctx context.Context, job string, environment map[string]string) (LaunchResult, error)
	CancelExecution(ctx context.Context, job, execution string) error
	ListTasks(ctx context.Context, job, execution string) ([]Task, error)
	ReadinessMarkers(ctx context.Context, job, execution string, kind Kind) ([]string, error)
}

// GCloudConfig configures a gcloud-backed Client. Project and Region must
// match the validated Request passed to Runner.
type GCloudConfig struct {
	Project        string
	Region         string
	Executable     string
	CommandTimeout time.Duration
	KillGrace      time.Duration
}

// GCloudClient invokes a fixed gcloud executable with direct argv. It never
// invokes a shell and never captures child stderr.
type GCloudClient struct {
	project    string
	region     string
	executable string
	timeout    time.Duration
	killGrace  time.Duration
	run        commandRunner
}

type commandRunner interface {
	Run(ctx context.Context, executable string, args []string, timeout, killGrace time.Duration, outputLimit int) commandResult
}

type commandResult struct {
	stdout   []byte
	exitCode int
	err      error
}

// NewGCloudClient constructs a bounded, direct-exec gcloud adapter.
func NewGCloudClient(config GCloudConfig) (*GCloudClient, error) {
	if !projectPattern.MatchString(config.Project) {
		return nil, &ValidationError{Field: projectEnvironment, Rule: "must be a valid project ID"}
	}
	if !regionPattern.MatchString(config.Region) {
		return nil, &ValidationError{Field: regionEnvironment, Rule: "must be a valid GCP region"}
	}
	if config.Executable == "" {
		config.Executable = "gcloud"
	}
	if strings.ContainsAny(config.Executable, "\r\n\x00") {
		return nil, &ValidationError{Field: "gcloud executable", Rule: "must be a valid path"}
	}
	executable, err := resolveExecutable(config.Executable)
	if err != nil {
		return nil, &ValidationError{Field: "gcloud executable", Rule: "must resolve to an executable regular file"}
	}
	if config.CommandTimeout == 0 {
		config.CommandTimeout = defaultCommandTimeout
	}
	if config.KillGrace == 0 {
		config.KillGrace = defaultKillGrace
	}
	if config.CommandTimeout < time.Second || config.CommandTimeout > 5*time.Minute {
		return nil, &ValidationError{Field: "command timeout", Rule: "must be between one second and five minutes"}
	}
	if config.KillGrace < time.Second || config.KillGrace > time.Minute {
		return nil, &ValidationError{Field: "command kill grace", Rule: "must be between one second and one minute"}
	}
	return &GCloudClient{
		project:    config.Project,
		region:     config.Region,
		executable: executable,
		timeout:    config.CommandTimeout,
		killGrace:  config.KillGrace,
		run:        osCommandRunner{},
	}, nil
}

func (client *GCloudClient) command(ctx context.Context, args ...string) commandResult {
	if client == nil || client.run == nil {
		return commandResult{exitCode: 125, err: errControlPlane}
	}
	return client.run.Run(ctx, client.executable, args, client.timeout, client.killGrace, maximumCommandOutput)
}

// ListExecutions returns at most maximum+1 records so Runner can detect a
// response that exceeds its fail-closed bound.
func (client *GCloudClient) ListExecutions(ctx context.Context, job string, maximum int) ([]Execution, error) {
	if !validJobName(job) || maximum < 1 || maximum > 10_000 {
		return nil, errControlPlane
	}
	result := client.command(ctx,
		"run", "jobs", "executions", "list",
		"--job", job,
		"--project", client.project,
		"--region", client.region,
		"--sort-by=~metadata.creationTimestamp",
		"--limit", strconv.Itoa(maximum+1),
		"--format=json(metadata.name,name,status.conditions,conditions,status.completionTime,completionTime,status.runningCount,runningCount,status.succeededCount,succeededCount,status.failedCount,failedCount,status.cancelledCount,cancelledCount,status.retriedCount,retriedCount)",
	)
	if result.err != nil || result.exitCode != 0 {
		return nil, errControlPlane
	}
	executions, err := parseExecutionList(result.stdout, client.project, client.region, job)
	if err != nil {
		return nil, errControlPlane
	}
	return executions, nil
}

func (client *GCloudClient) DescribeExecution(ctx context.Context, job, execution string) (Execution, error) {
	leaf, err := executionLeaf(client.project, client.region, job, execution)
	if err != nil {
		return Execution{}, errControlPlane
	}
	result := client.command(ctx,
		"run", "jobs", "executions", "describe", leaf,
		"--project", client.project,
		"--region", client.region,
		"--format=json",
	)
	if result.err != nil || result.exitCode != 0 {
		return Execution{}, errControlPlane
	}
	record, err := parseExecution(result.stdout, client.project, client.region, job)
	if err != nil || record.Name != leaf {
		return Execution{}, errControlPlane
	}
	return record, nil
}

func (client *GCloudClient) Execute(ctx context.Context, job string, environment map[string]string) (LaunchResult, error) {
	if !validJobName(job) || len(environment) == 0 || len(environment) > 3 {
		return LaunchResult{ExitCode: 125}, errControlPlane
	}
	keys := make([]string, 0, len(environment))
	for key, value := range environment {
		if !validOverride(key, value) {
			return LaunchResult{ExitCode: 125}, errControlPlane
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	overrides := make([]string, 0, len(keys))
	for _, key := range keys {
		overrides = append(overrides, key+"="+environment[key])
	}
	result := client.command(ctx,
		"run", "jobs", "execute", job,
		"--project", client.project,
		"--region", client.region,
		"--update-env-vars="+strings.Join(overrides, ","),
		"--async",
		"--format=value(metadata.name)",
		"--quiet",
	)
	candidate := strings.TrimSuffix(string(result.stdout), "\n")
	candidate = strings.TrimSuffix(candidate, "\r")
	launch := LaunchResult{Candidate: candidate, ExitCode: result.exitCode}
	if result.err != nil || result.exitCode != 0 {
		if launch.ExitCode <= 0 || launch.ExitCode > 125 {
			launch.ExitCode = 125
		}
		return launch, errControlPlane
	}
	return launch, nil
}

func (client *GCloudClient) CancelExecution(ctx context.Context, job, execution string) error {
	leaf, err := executionLeaf(client.project, client.region, job, execution)
	if err != nil {
		return errControlPlane
	}
	result := client.command(ctx,
		"run", "jobs", "executions", "cancel", leaf,
		"--project", client.project,
		"--region", client.region,
		"--async",
		"--quiet",
	)
	if result.err != nil || result.exitCode != 0 {
		return errControlPlane
	}
	return nil
}

func (client *GCloudClient) ListTasks(ctx context.Context, job, execution string) ([]Task, error) {
	leaf, err := executionLeaf(client.project, client.region, job, execution)
	if err != nil {
		return nil, errControlPlane
	}
	result := client.command(ctx,
		"run", "jobs", "executions", "tasks", "list",
		"--execution", leaf,
		"--project", client.project,
		"--region", client.region,
		"--limit", "4",
		"--format=json",
	)
	if result.err != nil || result.exitCode != 0 {
		return nil, errControlPlane
	}
	tasks, err := parseTasks(result.stdout)
	if err != nil {
		return nil, errControlPlane
	}
	return tasks, nil
}

func (client *GCloudClient) ReadinessMarkers(ctx context.Context, job, execution string, kind Kind) ([]string, error) {
	leaf, err := executionLeaf(client.project, client.region, job, execution)
	if err != nil || !validJobForKind(job, kind) {
		return nil, errControlPlane
	}
	filter := fmt.Sprintf(
		`resource.type="cloud_run_job" AND resource.labels.job_name="%s" AND resource.labels.location="%s" AND labels."run.googleapis.com/execution_name"="%s"`,
		job,
		client.region,
		leaf,
	)
	result := client.command(ctx,
		"logging", "read", filter,
		"--project", client.project,
		"--freshness", "2h",
		"--order", "asc",
		"--limit", strconv.Itoa(maximumLogEntries),
		"--format=json",
	)
	if result.err != nil || result.exitCode != 0 {
		return nil, errControlPlane
	}
	markers, err := parseLogMarkers(result.stdout, leaf, kind)
	if err != nil {
		return nil, errControlPlane
	}
	return markers, nil
}

func validJobName(job string) bool {
	return validJobForKind(job, KindBackend) || validJobForKind(job, KindBrowser) || validJobForKind(job, KindOCR)
}

func resolveExecutable(candidate string) (string, error) {
	resolved, err := exec.LookPath(candidate)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errControlPlane
	}
	return resolved, nil
}

func validOverride(key, value string) bool {
	switch key {
	case "SCRIBE_READINESS_EXECUTION_ID":
		return markerPattern.MatchString(value)
	case browserSecretVersionEnvironment:
		version, err := strconv.ParseUint(value, 10, 64)
		return secretVersionPattern.MatchString(value) && err == nil && version >= 2
	case browserStateDigestEnvironment:
		return stateDigestPattern.MatchString(value)
	default:
		return false
	}
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, executable string, args []string, timeout, killGrace time.Duration, outputLimit int) commandResult {
	callContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- executable is configuration-controlled and argv values are
	// validated before this direct exec; no shell is involved.
	command := exec.CommandContext(callContext, executable, args...)
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := command.Process.Signal(syscall.SIGTERM)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	command.WaitDelay = killGrace
	buffer := newBoundedBuffer(outputLimit)
	command.Stdout = buffer
	command.Stderr = io.Discard
	command.Stdin = nil
	err := command.Run()
	if buffer.exceeded {
		return commandResult{stdout: buffer.Bytes(), exitCode: 125, err: errOutputLimit}
	}
	if err == nil {
		return commandResult{stdout: buffer.Bytes(), exitCode: 0}
	}
	exitCode := 125
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		exitCode = exitError.ExitCode()
		if exitCode <= 0 || exitCode > 125 {
			exitCode = 125
		}
	}
	return commandResult{stdout: buffer.Bytes(), exitCode: exitCode, err: errControlPlane}
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
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

type taskWire struct {
	Index   json.RawMessage `json:"index"`
	Retried json.RawMessage `json:"retried"`
	Status  struct {
		Index       json.RawMessage `json:"index"`
		Retried     json.RawMessage `json:"retried"`
		LastAttempt taskResultWire  `json:"lastAttemptResult"`
	} `json:"status"`
	LastAttempt taskResultWire `json:"lastAttemptResult"`
}

type taskResultWire struct {
	ExitCode   json.RawMessage `json:"exitCode"`
	TermSignal json.RawMessage `json:"termSignal"`
	Status     struct {
		Code json.RawMessage `json:"code"`
	} `json:"status"`
}

func parseTasks(data []byte) ([]Task, error) {
	var wires []taskWire
	if err := decodeStrictJSON(data, &wires); err != nil || wires == nil || len(wires) > 4 {
		return nil, errInvalidModel
	}
	tasks := make([]Task, 0, len(wires))
	for _, wire := range wires {
		result := wire.Status.LastAttempt
		if rawMissing(result.ExitCode) && rawMissing(result.TermSignal) && rawMissing(result.Status.Code) {
			result = wire.LastAttempt
		}
		task := Task{
			Index:      parseOptionalBounded(firstRaw(wire.Status.Index, wire.Index), 63),
			Retried:    parseOptionalBounded(firstRaw(wire.Status.Retried, wire.Retried), 63),
			ExitCode:   parseOptionalBounded(result.ExitCode, 255),
			TermSignal: parseOptionalBounded(result.TermSignal, 64),
			StatusCode: parseOptionalBounded(result.Status.Code, 16),
		}
		if _, err := validateTasks([]Task{task}); err != nil {
			return nil, errInvalidModel
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func firstRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if !rawMissing(value) {
			return value
		}
	}
	return nil
}

func rawMissing(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

func parseOptionalBounded(raw json.RawMessage, maximum int) int {
	if rawMissing(raw) {
		return -1
	}
	value, err := parseCountWithMaximum(raw, maximum)
	if err != nil {
		return -2
	}
	return value
}

func parseCountWithMaximum(raw json.RawMessage, maximum int) (int, error) {
	value, err := strconv.ParseInt(strings.Trim(string(raw), `"`), 10, 32)
	if err != nil || value < 0 || value > int64(maximum) {
		return 0, errInvalidModel
	}
	return int(value), nil
}

type logWire struct {
	TextPayload string            `json:"textPayload"`
	Labels      map[string]string `json:"labels"`
}

func parseLogMarkers(data []byte, execution string, kind Kind) ([]string, error) {
	var logs []logWire
	if err := decodeStrictJSON(data, &logs); err != nil || logs == nil || len(logs) > maximumLogEntries {
		return nil, errInvalidModel
	}
	markers := make([]string, 0, maximumLogMarkers)
	for _, log := range logs {
		if log.Labels["run.googleapis.com/execution_name"] != execution {
			continue
		}
		line := strings.TrimSuffix(strings.TrimSuffix(log.TextPayload, "\n"), "\r")
		if ReadinessMarkerAllowed(kind, line) {
			markers = append(markers, line)
			if len(markers) == maximumLogMarkers {
				break
			}
		}
	}
	return markers, nil
}

var (
	readinessErrorKind   = `(Error|TypeError|AbortError|TimeoutError)(/(EACCES|EADDRINUSE|EAI_AGAIN|ECONNREFUSED|ECONNRESET|EHOSTUNREACH|ENETUNREACH|ENOTFOUND|EPIPE|ENOENT|ETIMEDOUT|ERR_STREAM_PREMATURE_CLOSE))?`
	readinessPayloadKind = `(invalid-json|invalid-payload|non-ready-status|missing-status|api-image-mismatch|public-origin-mismatch|ready)`
	backendPayloadKind   = `(invalid-json|invalid-payload|invalid-public-origin|non-ready-status|missing-status|ready-payload-with-non-success-http)`
	backendHTTPKind      = `(http-ready|http-non-ready|http-invalid|http-error|http-timeout|http-transport-(EACCES|EADDRINUSE|EAI_AGAIN|ECONNREFUSED|ECONNRESET|EHOSTUNREACH|ENETUNREACH|ENOTFOUND|EPIPE|ENOENT|ETIMEDOUT|ERR_STREAM_PREMATURE_CLOSE|error))`
	backendNetworkKind   = `((dns-match|dns-mismatch|dns-empty|dns-timeout|dns-error); (tcp-open|tcp-refused|tcp-timeout|tcp-unreachable|tcp-error); ` + backendHTTPKind + `|dns-invalid-origin; tcp-skipped; http-skipped)`
	backendMarkerPattern = regexp.MustCompile(`^(frontend readiness failed: (frontend-server-exited|frontend did not respond|HTTP [1-5][0-9]{2} \(` + readinessPayloadKind + `\)|transport-` + readinessErrorKind + `|internal-` + readinessErrorKind + `)|frontend proxy request failed \[` + readinessErrorKind + `\]|frontend backend startup gate failed \[` + readinessErrorKind + `; (readiness-contract; HTTP [1-5][0-9]{2} \((invalid-json|invalid-payload|invalid-public-origin|missing-status|ready-payload-with-non-success-http)\)|startup-deadline; (backend did not report ready|HTTP [1-5][0-9]{2} \(` + backendPayloadKind + `\)|transport-` + readinessErrorKind + `))\]|frontend backend network probe \[` + backendNetworkKind + `\])$`)
	ocrMarkerPattern     = regexp.MustCompile(`^ocr readiness failed: (image-contract|(segment|transcribe|ollama)-(token|request|timeout|contract))$`)
	browserMarkerPattern = regexp.MustCompile(`^browser readiness failed: (home|context|upload|upload-multi|handoff|transcription|annotations|editor|overlay|retranscribe|structure|save|publish|responsive|token|manifest|cleanup|network|network-(document|auth|workspace|item|context|annotation|processing|transcription|events|presentation|iiif|asset|other)-(client|server)|network-(document|api|events|image|asset|other)-transport|initial-ingress-(forbidden|not-found)|csp|rate)$`)
)

var (
	browserUploadSubstageMarkerPattern          = regexp.MustCompile(`^browser readiness upload substage: (start-response|start-transport|image-terminal|image-retry|image-transport|handoff-timeout|handoff-terminal|response-contract)$`)
	browserUploadDurableFailureMarkerPattern    = regexp.MustCompile(`^browser readiness upload durable failure: (segmentation-canceled|segmentation-timeout|segmentation-failed|provider-authentication|provider-failed|admission-failed|upload-storage-failed|segmentation-output-failed|quota-resize-failed|lease-renewal-failed|image-commit-failed|ocr-run-commit-failed|annotation-commit-failed|transcription-enqueue-failed|item-reload-failed|batch-commit-failed|unknown)$`)
	browserUploadRetryableResponseMarkerPattern = regexp.MustCompile(`^browser readiness upload retryable response: (connect-(aborted|already-exists|deadline-exceeded|internal|resource-exhausted|unavailable|unknown)|http-(408|409|425|429|500|502|503|504))$`)
	browserStructureSubstageMarkerPattern       = regexp.MustCompile(`^browser readiness structure substage: (draw-mode|centered-line|undo-redo|delete-line|line-edit|split-words|add-word|word-history|join-words|split-line|join-lines|snapshot)$`)
	browserTokenSubstageMarkerPattern           = regexp.MustCompile(`^browser readiness token substage: (post-home-presentation|settings-open|key-creation|key-display|key-display-copy|key-display-done|key-display-clear|key-deletion|logout-proof|final-cleanup)$`)
	browserManifestSubstageMarkerPattern        = regexp.MustCompile(`^browser readiness manifest substage: (library-navigation|import-form|import-request|import-request-body|import-upstream-request|import-upstream-response|import-response-delivery|import-response-status|import-response-settlement|import-response-(connect-(aborted|already-exists|canceled|data-loss|deadline-exceeded|failed-precondition|internal|invalid-argument|not-found|out-of-range|permission-denied|resource-exhausted|unauthenticated|unavailable|unimplemented|unknown)|http-(400|401|403|404|408|409|425|429|500|502|503|504|other-4xx|other-5xx))|import-contract|editor-navigation|editor-mount|first-canvas|first-image|first-annotations|first-publication|second-image|second-canvas|second-annotations|second-overlay|second-publication)$`)
	browserRateLimitMarkerPattern               = regexp.MustCompile(`^browser readiness rate limit: (document|auth|workspace|item|context|annotation|processing|transcription|events|presentation|iiif|asset|other)$`)
)

// ReadinessMarkerAllowed reports whether a complete log line belongs to the
// public, exact readiness error vocabulary for kind.
func ReadinessMarkerAllowed(kind Kind, line string) bool {
	if line == "" || len(line) > 512 || strings.ContainsAny(line, "\r\n") {
		return false
	}
	switch kind {
	case KindBackend:
		return backendMarkerPattern.MatchString(line)
	case KindOCR:
		return ocrMarkerPattern.MatchString(line)
	case KindBrowser:
		return browserMarkerPattern.MatchString(line) ||
			browserUploadSubstageMarkerPattern.MatchString(line) ||
			browserUploadDurableFailureMarkerPattern.MatchString(line) ||
			browserUploadRetryableResponseMarkerPattern.MatchString(line) ||
			browserStructureSubstageMarkerPattern.MatchString(line) ||
			browserTokenSubstageMarkerPattern.MatchString(line) ||
			browserManifestSubstageMarkerPattern.MatchString(line) ||
			browserRateLimitMarkerPattern.MatchString(line)
	default:
		return false
	}
}
