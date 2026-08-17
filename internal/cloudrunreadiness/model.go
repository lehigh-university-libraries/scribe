package cloudrunreadiness

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// ExecutionState is the normalized terminal state of a Cloud Run execution.
type ExecutionState string

const (
	ExecutionRunning   ExecutionState = "running"
	ExecutionSucceeded ExecutionState = "succeeded"
	ExecutionFailed    ExecutionState = "failed"
	ExecutionCancelled ExecutionState = "cancelled"
	ExecutionUnknown   ExecutionState = "unknown"
)

// Execution contains only bounded, typed fields needed by the lifecycle and
// diagnostics code. Arbitrary Cloud Run payload fields are intentionally not
// retained.
type Execution struct {
	Name            string
	CreateTime      string
	StartTime       string
	CompletionTime  string
	RunningCount    int
	SucceededCount  int
	FailedCount     int
	CancelledCount  int
	RetriedCount    int
	State           ExecutionState
	Reason          string
	ReadinessMarker string
}

// Task contains the bounded task outcome fields permitted in diagnostics.
type Task struct {
	Index      int
	Retried    int
	ExitCode   int
	TermSignal int
	StatusCode int
}

var (
	executionSuffixPattern = regexp.MustCompile(`^[a-z0-9]{5}$`)
	projectNumberPattern   = regexp.MustCompile(`^[1-9][0-9]{5,19}$`)
	timestampPattern       = regexp.MustCompile(`^[0-9TZ:+.-]{10,40}$`)
	markerPattern          = regexp.MustCompile(`^readiness-[1-9][0-9]*-[A-Za-z0-9]{6}$`)
	nonAlphaNumericPattern = regexp.MustCompile(`[^a-z0-9]`)
)

var errInvalidModel = errors.New("invalid Cloud Run response")

func executionLeaf(project, region, job, candidate string) (string, error) {
	if candidate == "" || len(candidate) > 512 || strings.ContainsAny(candidate, "\r\n") {
		return "", errInvalidModel
	}
	leaf := candidate
	if slash := strings.LastIndex(candidate, "/"); slash >= 0 {
		leaf = candidate[slash+1:]
	}
	expectedPrefix := job + "-"
	if !strings.HasPrefix(leaf, expectedPrefix) || !executionSuffixPattern.MatchString(strings.TrimPrefix(leaf, expectedPrefix)) {
		return "", errInvalidModel
	}
	canonical := "projects/" + project + "/locations/" + region + "/jobs/" + job + "/executions/" + leaf
	if candidate != leaf && candidate != canonical {
		return "", errInvalidModel
	}
	return leaf, nil
}

func apiExecutionLeaf(project, region, job, candidate string) (string, error) {
	if candidate == "" || len(candidate) > 512 || strings.ContainsAny(candidate, "\r\n") {
		return "", errInvalidModel
	}
	leaf := candidate
	if slash := strings.LastIndex(candidate, "/"); slash >= 0 {
		leaf = candidate[slash+1:]
	}
	expectedPrefix := job + "-"
	if !strings.HasPrefix(leaf, expectedPrefix) || !executionSuffixPattern.MatchString(strings.TrimPrefix(leaf, expectedPrefix)) {
		return "", errInvalidModel
	}
	if candidate == leaf {
		return leaf, nil
	}
	prefix := "projects/"
	suffix := "/locations/" + region + "/jobs/" + job + "/executions/" + leaf
	if !strings.HasPrefix(candidate, prefix) || !strings.HasSuffix(candidate, suffix) {
		return "", errInvalidModel
	}
	resourceProject := strings.TrimSuffix(strings.TrimPrefix(candidate, prefix), suffix)
	if resourceProject != project && !projectNumberPattern.MatchString(resourceProject) {
		return "", errInvalidModel
	}
	return leaf, nil
}

func validateExecution(execution Execution, project, region, job string) (Execution, error) {
	leaf, err := executionLeaf(project, region, job, execution.Name)
	if err != nil {
		return Execution{}, err
	}
	execution.Name = leaf
	switch execution.State {
	case ExecutionRunning, ExecutionSucceeded, ExecutionFailed, ExecutionCancelled:
	default:
		return Execution{}, errInvalidModel
	}
	for _, count := range []int{
		execution.RunningCount,
		execution.SucceededCount,
		execution.FailedCount,
		execution.CancelledCount,
		execution.RetriedCount,
	} {
		if count < 0 || count > 64 {
			return Execution{}, errInvalidModel
		}
	}
	if execution.ReadinessMarker != "" && !markerPattern.MatchString(execution.ReadinessMarker) {
		return Execution{}, errInvalidModel
	}
	execution.CreateTime = safeTimestamp(execution.CreateTime)
	execution.StartTime = safeTimestamp(execution.StartTime)
	execution.CompletionTime = safeTimestamp(execution.CompletionTime)
	execution.Reason = safeReason(execution.Reason)
	return execution, nil
}

func validateTasks(tasks []Task) ([]Task, error) {
	if len(tasks) > 4 {
		return nil, errInvalidModel
	}
	validated := make([]Task, len(tasks))
	copy(validated, tasks)
	for _, task := range validated {
		if task.Index < -1 || task.Index > 63 ||
			task.Retried < -1 || task.Retried > 63 ||
			task.ExitCode < -1 || task.ExitCode > 255 ||
			task.TermSignal < -1 || task.TermSignal > 64 ||
			task.StatusCode < -1 || task.StatusCode > 16 {
			return nil, errInvalidModel
		}
	}
	return validated, nil
}

func safeTimestamp(value string) string {
	if timestampPattern.MatchString(value) {
		return value
	}
	return "unknown"
}

func safeReason(value string) string {
	normalized := nonAlphaNumericPattern.ReplaceAllString(strings.ToLower(value), "")
	switch normalized {
	case "nonzeroexitcode", "nonzeroexit":
		return "non-zero-exit"
	case "cancelled", "cancelling":
		return "cancelled"
	case "progressdeadlineexceeded", "deadline":
		return "deadline"
	case "jobstatusservicepollingerror", "internal", "platform":
		return "platform"
	default:
		return "unknown"
	}
}

type executionWire struct {
	Name       string          `json:"name"`
	CreateTime string          `json:"createTime"`
	StartTime  string          `json:"startTime"`
	Completion string          `json:"completionTime"`
	Conditions []conditionWire `json:"conditions"`
	Running    json.RawMessage `json:"runningCount"`
	Succeeded  json.RawMessage `json:"succeededCount"`
	Failed     json.RawMessage `json:"failedCount"`
	Cancelled  json.RawMessage `json:"cancelledCount"`
	Retried    json.RawMessage `json:"retriedCount"`
	Template   templateWire    `json:"template"`
	Metadata   struct {
		Name       string `json:"name"`
		CreateTime string `json:"creationTimestamp"`
	} `json:"metadata"`
	Status struct {
		StartTime  string          `json:"startTime"`
		Completion string          `json:"completionTime"`
		Conditions []conditionWire `json:"conditions"`
		Running    json.RawMessage `json:"runningCount"`
		Succeeded  json.RawMessage `json:"succeededCount"`
		Failed     json.RawMessage `json:"failedCount"`
		Cancelled  json.RawMessage `json:"cancelledCount"`
		Retried    json.RawMessage `json:"retriedCount"`
	} `json:"status"`
	Spec struct {
		Template struct {
			Containers []containerWire `json:"containers"`
			Spec       templateWire    `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

type conditionWire struct {
	Type            string `json:"type"`
	Status          string `json:"status"`
	State           string `json:"state"`
	Reason          string `json:"reason"`
	ExecutionReason string `json:"executionReason"`
}

type templateWire struct {
	Containers []containerWire `json:"containers"`
}

type containerWire struct {
	Env []envWire `json:"env"`
}

type envWire struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func parseExecution(data []byte, project, region, job string) (Execution, error) {
	var wire executionWire
	if err := decodeStrictJSON(data, &wire); err != nil {
		return Execution{}, errInvalidModel
	}
	return normalizeExecution(wire, project, region, job)
}

func parseExecutionList(data []byte, project, region, job string) ([]Execution, error) {
	var wires []executionWire
	if err := decodeStrictJSON(data, &wires); err != nil || wires == nil {
		return nil, errInvalidModel
	}
	executions := make([]Execution, 0, len(wires))
	seen := make(map[string]struct{}, len(wires))
	for _, wire := range wires {
		execution, err := normalizeExecution(wire, project, region, job)
		if err != nil {
			return nil, errInvalidModel
		}
		if _, exists := seen[execution.Name]; exists {
			return nil, errInvalidModel
		}
		seen[execution.Name] = struct{}{}
		executions = append(executions, execution)
	}
	return executions, nil
}

func normalizeExecution(wire executionWire, project, region, job string) (Execution, error) {
	name := wire.Metadata.Name
	if name == "" {
		name = wire.Name
	}
	leaf, err := apiExecutionLeaf(project, region, job, name)
	if err != nil {
		return Execution{}, errInvalidModel
	}
	name = leaf
	createTime := wire.Metadata.CreateTime
	if createTime == "" {
		createTime = wire.CreateTime
	}
	startTime := wire.Status.StartTime
	if startTime == "" {
		startTime = wire.StartTime
	}
	completionTime := wire.Status.Completion
	if completionTime == "" {
		completionTime = wire.Completion
	}
	conditions := wire.Status.Conditions
	if len(conditions) == 0 {
		conditions = wire.Conditions
	}

	running, err := firstCount(wire.Status.Running, wire.Running)
	if err != nil {
		return Execution{}, errInvalidModel
	}
	succeeded, err := firstCount(wire.Status.Succeeded, wire.Succeeded)
	if err != nil {
		return Execution{}, errInvalidModel
	}
	failed, err := firstCount(wire.Status.Failed, wire.Failed)
	if err != nil {
		return Execution{}, errInvalidModel
	}
	cancelled, err := firstCount(wire.Status.Cancelled, wire.Cancelled)
	if err != nil {
		return Execution{}, errInvalidModel
	}
	retried, err := firstCount(wire.Status.Retried, wire.Retried)
	if err != nil {
		return Execution{}, errInvalidModel
	}

	completed := conditionWire{}
	for _, condition := range conditions {
		if condition.Type == "Completed" {
			completed = condition
			break
		}
	}
	reason := completed.Reason
	if reason == "" {
		reason = completed.ExecutionReason
	}
	state := classifyState(completed, completionTime, succeeded, failed, cancelled)
	marker, err := findMarker(wire)
	if err != nil {
		return Execution{}, errInvalidModel
	}

	return validateExecution(Execution{
		Name:            name,
		CreateTime:      createTime,
		StartTime:       startTime,
		CompletionTime:  completionTime,
		RunningCount:    running,
		SucceededCount:  succeeded,
		FailedCount:     failed,
		CancelledCount:  cancelled,
		RetriedCount:    retried,
		State:           state,
		Reason:          reason,
		ReadinessMarker: marker,
	}, project, region, job)
}

func classifyState(completed conditionWire, completionTime string, succeeded, failed, cancelled int) ExecutionState {
	reason := nonAlphaNumericPattern.ReplaceAllString(strings.ToLower(firstNonempty(completed.Reason, completed.ExecutionReason)), "")
	if reason == "cancelling" {
		return ExecutionRunning
	}
	switch {
	case completed.Status == "True" || completed.State == "CONDITION_SUCCEEDED":
		return ExecutionSucceeded
	case completed.Status == "False" || completed.State == "CONDITION_FAILED":
		if reason == "cancelled" || cancelled > 0 {
			return ExecutionCancelled
		}
		return ExecutionFailed
	case completed.Status == "Unknown" || completed.State == "CONDITION_RECONCILING" || completed.State == "CONDITION_PENDING":
		return ExecutionRunning
	case completionTime != "":
		switch {
		case cancelled > 0:
			return ExecutionCancelled
		case failed > 0:
			return ExecutionFailed
		case succeeded > 0:
			return ExecutionSucceeded
		default:
			return ExecutionUnknown
		}
	default:
		return ExecutionRunning
	}
}

func findMarker(wire executionWire) (string, error) {
	containers := make([]containerWire, 0, len(wire.Spec.Template.Spec.Containers)+len(wire.Spec.Template.Containers)+len(wire.Template.Containers))
	containers = append(containers, wire.Spec.Template.Spec.Containers...)
	containers = append(containers, wire.Spec.Template.Containers...)
	containers = append(containers, wire.Template.Containers...)
	var marker string
	for _, container := range containers {
		for _, environment := range container.Env {
			if environment.Name != "SCRIBE_READINESS_EXECUTION_ID" {
				continue
			}
			if !markerPattern.MatchString(environment.Value) || (marker != "" && marker != environment.Value) {
				return "", errInvalidModel
			}
			marker = environment.Value
		}
	}
	return marker, nil
}

func firstCount(primary, fallback json.RawMessage) (int, error) {
	if len(primary) > 0 && string(primary) != "null" {
		return parseCount(primary)
	}
	if len(fallback) > 0 && string(fallback) != "null" {
		return parseCount(fallback)
	}
	return 0, nil
}

func parseCount(raw json.RawMessage) (int, error) {
	var number json.Number
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, errInvalidModel
		}
		number = json.Number(value)
	} else {
		number = json.Number(string(raw))
	}
	value, err := strconv.ParseInt(number.String(), 10, 32)
	if err != nil || value < 0 || value > 64 {
		return 0, errInvalidModel
	}
	return int(value), nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errInvalidModel
	}
	return nil
}
