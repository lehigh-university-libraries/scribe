// Package productionbrowserreadiness owns the production browser credential
// transport and its fail-closed cleanup lifecycle.
package productionbrowserreadiness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	cloudProjectEnvironment      = "GCLOUD_PROJECT"
	deploymentEnvironment        = "SCRIBE_DEPLOYMENT_ENVIRONMENT"
	regionEnvironment            = "SCRIBE_REGION"
	zoneEnvironment              = "SCRIBE_ZONE"
	instanceEnvironment          = "SCRIBE_INSTANCE"
	runIDEnvironment             = "GITHUB_RUN_ID"
	runAttemptEnvironment        = "GITHUB_RUN_ATTEMPT"
	temporaryRootEnvironment     = "RUNNER_TEMP"
	cloudReadinessBinEnvironment = "SCRIBE_CLOUD_RUN_READINESS_BIN"
)

var (
	projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	regionPattern  = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)
	zonePattern    = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+-[a-z]$`)
	jobPattern     = regexp.MustCompile(`^scribe-browser-([0-9a-f]{8})$`)
	secretPattern  = regexp.MustCompile(`^scribe-browser-session-([0-9a-f]{8})$`)
	runIDPattern   = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	attemptPattern = regexp.MustCompile(`^[1-9][0-9]{0,4}$`)
)

// Request is one validated production browser readiness invocation.
type Request struct {
	Project                  string
	Region                   string
	Zone                     string
	Instance                 string
	RunID                    string
	RunAttempt               string
	Job                      string
	Secret                   string
	DiagnosticsPath          string
	TemporaryRoot            string
	TransportExecutable      string
	CloudReadinessExecutable string
}

// ValidationError is safe to render because it never embeds rejected input.
type ValidationError struct {
	Field string
	Rule  string
}

func (err *ValidationError) Error() string {
	if err == nil {
		return "invalid production browser readiness request"
	}
	return fmt.Sprintf("%s %s", err.Field, err.Rule)
}

// IsValidationError reports whether err is a request boundary failure.
func IsValidationError(err error) bool {
	var validationError *ValidationError
	return errors.As(err, &validationError)
}

// ParseRequest validates argv and protected workflow environment metadata.
func ParseRequest(args []string, getenv func(string) string, workingDirectory, transportExecutable string) (Request, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if len(args) != 3 {
		return Request{}, &ValidationError{Field: "arguments", Rule: "must be JOB SECRET DIAGNOSTICS_FILE"}
	}
	if workingDirectory == "" {
		var err error
		workingDirectory, err = os.Getwd()
		if err != nil {
			return Request{}, &ValidationError{Field: "working directory", Rule: "must be available"}
		}
	}
	diagnosticsPath := args[2]
	if !filepath.IsAbs(diagnosticsPath) {
		diagnosticsPath = filepath.Join(workingDirectory, diagnosticsPath)
	}
	if !filepath.IsAbs(transportExecutable) {
		return Request{}, &ValidationError{Field: "transport executable", Rule: "must be an absolute regular file"}
	}
	temporaryRoot := getenv(temporaryRootEnvironment)
	if temporaryRoot == "" {
		temporaryRoot = os.TempDir()
	}
	request := Request{
		Project:                  getenv(cloudProjectEnvironment),
		Region:                   getenv(regionEnvironment),
		Zone:                     getenv(zoneEnvironment),
		Instance:                 getenv(instanceEnvironment),
		RunID:                    getenv(runIDEnvironment),
		RunAttempt:               getenv(runAttemptEnvironment),
		Job:                      args[0],
		Secret:                   args[1],
		DiagnosticsPath:          filepath.Clean(diagnosticsPath),
		TemporaryRoot:            temporaryRoot,
		TransportExecutable:      transportExecutable,
		CloudReadinessExecutable: getenv(cloudReadinessBinEnvironment),
	}
	if getenv(deploymentEnvironment) != "production" {
		return Request{}, &ValidationError{Field: deploymentEnvironment, Rule: "must be production"}
	}
	if err := ValidateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

// ValidateRequest applies the command boundary to programmatic requests.
func ValidateRequest(request Request) error {
	if !projectPattern.MatchString(request.Project) {
		return &ValidationError{Field: cloudProjectEnvironment, Rule: "must be a valid project ID"}
	}
	if !regionPattern.MatchString(request.Region) {
		return &ValidationError{Field: regionEnvironment, Rule: "must be a valid GCP region"}
	}
	if !zonePattern.MatchString(request.Zone) || !strings.HasPrefix(request.Zone, request.Region+"-") {
		return &ValidationError{Field: zoneEnvironment, Rule: "must belong to the configured region"}
	}
	if request.Instance != "scribe" {
		return &ValidationError{Field: instanceEnvironment, Rule: "must identify the production instance"}
	}
	if !runIDPattern.MatchString(request.RunID) || !attemptPattern.MatchString(request.RunAttempt) {
		return &ValidationError{Field: "run identity", Rule: "must be canonical positive decimal metadata"}
	}
	jobMatch := jobPattern.FindStringSubmatch(request.Job)
	secretMatch := secretPattern.FindStringSubmatch(request.Secret)
	if len(jobMatch) != 2 {
		return &ValidationError{Field: "JOB", Rule: "must identify a production browser readiness job"}
	}
	if len(secretMatch) != 2 || secretMatch[1] != jobMatch[1] {
		return &ValidationError{Field: "SECRET", Rule: "must be paired with JOB"}
	}
	if err := validateDiagnosticsPath(request.DiagnosticsPath); err != nil {
		return err
	}
	if err := validateDirectory(request.TemporaryRoot, temporaryRootEnvironment); err != nil {
		return err
	}
	if err := validateRegularFile(request.TransportExecutable, "transport executable", true); err != nil {
		return err
	}
	if err := validateRegularFile(request.CloudReadinessExecutable, cloudReadinessBinEnvironment, true); err != nil {
		return err
	}
	return nil
}

func validateDiagnosticsPath(path string) error {
	if path == "" || strings.ContainsAny(path, "\r\n\x00") || filepath.Ext(path) != ".log" {
		return &ValidationError{Field: "DIAGNOSTICS_FILE", Rule: "must be a single-line .log path"}
	}
	if err := validateDirectory(filepath.Dir(path), "DIAGNOSTICS_FILE directory"); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return &ValidationError{Field: "DIAGNOSTICS_FILE", Rule: "must be absent or a regular file"}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &ValidationError{Field: "DIAGNOSTICS_FILE", Rule: "must be inspectable"}
	}
	return nil
}

func validateDirectory(path, field string) error {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return &ValidationError{Field: field, Rule: "must be an absolute directory"}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &ValidationError{Field: field, Rule: "must be a non-symlink directory"}
	}
	return nil
}

func validateRegularFile(path, field string, executable bool) error {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return &ValidationError{Field: field, Rule: "must be an absolute regular file"}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return &ValidationError{Field: field, Rule: "must be a non-symlink regular file"}
	}
	if executable && info.Mode().Perm()&0o111 == 0 {
		return &ValidationError{Field: field, Rule: "must be executable"}
	}
	return nil
}
