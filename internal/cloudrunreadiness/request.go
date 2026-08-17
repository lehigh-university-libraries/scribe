package cloudrunreadiness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Kind identifies the readiness contract exercised by a Cloud Run job.
type Kind string

const (
	KindBackend Kind = "backend"
	KindBrowser Kind = "browser"
	KindOCR     Kind = "ocr"
)

const (
	projectEnvironment = "GCLOUD_PROJECT"
	regionEnvironment  = "SCRIBE_REGION"
	// #nosec G101 -- Environment variable name for a numeric version, not a credential.
	browserSecretVersionEnvironment = "SCRIBE_BROWSER_EXPECTED_SECRET_VERSION"
	browserStateDigestEnvironment   = "SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256"
)

var (
	projectPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	regionPattern        = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)
	serviceJobPattern    = regexp.MustCompile(`^scribe(?:-pr-[1-9][0-9]*)?-(?:prod|pr-[1-9][0-9]*)-(backend|ocr)-readiness$`)
	previewBrowserJob    = regexp.MustCompile(`^scribe-pr-[1-9][0-9]*-browser-[0-9a-f]{8}$`)
	productionBrowserJob = regexp.MustCompile(`^scribe-browser-[0-9a-f]{8}$`)
	secretVersionPattern = regexp.MustCompile(`^([2-9]|[1-9][0-9]{1,19})$`)
	stateDigestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Request is a validated invocation of the readiness runner.
type Request struct {
	Project                           string
	Region                            string
	Job                               string
	Kind                              Kind
	DiagnosticsPath                   string
	PreflightOnly                     bool
	BrowserExpectedSecretVersion      string
	BrowserExpectedStorageStateSHA256 string
}

// ValidationError is safe to show to an operator. It never includes rejected
// input or environment values.
type ValidationError struct {
	Field string
	Rule  string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "invalid readiness request"
	}
	return fmt.Sprintf("%s %s", e.Field, e.Rule)
}

// IsValidationError reports whether err represents invalid invocation input.
func IsValidationError(err error) bool {
	var validationError *ValidationError
	return errors.As(err, &validationError)
}

// ParseRequest parses the command-line contract and validates all environment
// metadata before any Cloud Run operation is attempted.
func ParseRequest(args []string, getenv func(string) string) (Request, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	request := Request{
		Project: getenv(projectEnvironment),
		Region:  getenv(regionEnvironment),
	}
	switch {
	case len(args) == 3 && args[0] == "--preflight-only":
		request.PreflightOnly = true
		request.Job = args[1]
		request.Kind = Kind(args[2])
	case len(args) == 3:
		request.Job = args[0]
		request.Kind = Kind(args[1])
		request.DiagnosticsPath = args[2]
		request.BrowserExpectedSecretVersion = getenv(browserSecretVersionEnvironment)
		request.BrowserExpectedStorageStateSHA256 = getenv(browserStateDigestEnvironment)
	default:
		return Request{}, &ValidationError{Field: "arguments", Rule: "must be JOB KIND DIAGNOSTICS_FILE or --preflight-only JOB KIND"}
	}

	if err := ValidateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

// ValidateRequest enforces the same boundary for programmatically constructed
// requests as ParseRequest does for CLI requests.
func ValidateRequest(request Request) error {
	if !projectPattern.MatchString(request.Project) {
		return &ValidationError{Field: projectEnvironment, Rule: "must be a valid project ID"}
	}
	if !regionPattern.MatchString(request.Region) {
		return &ValidationError{Field: regionEnvironment, Rule: "must be a valid GCP region"}
	}
	if !validJobForKind(request.Job, request.Kind) {
		return &ValidationError{Field: "JOB", Rule: "must identify the requested Scribe readiness kind"}
	}

	if request.PreflightOnly {
		if request.DiagnosticsPath != "" {
			return &ValidationError{Field: "DIAGNOSTICS_FILE", Rule: "must be omitted in preflight-only mode"}
		}
		return nil
	}

	if productionBrowserJob.MatchString(request.Job) {
		version, err := strconv.ParseUint(request.BrowserExpectedSecretVersion, 10, 64)
		if !secretVersionPattern.MatchString(request.BrowserExpectedSecretVersion) || err != nil || version < 2 {
			return &ValidationError{Field: browserSecretVersionEnvironment, Rule: "must be an exact production secret version of at least 2"}
		}
		if !stateDigestPattern.MatchString(request.BrowserExpectedStorageStateSHA256) {
			return &ValidationError{Field: browserStateDigestEnvironment, Rule: "must be an exact lowercase SHA-256 digest"}
		}
	} else if request.Kind == KindBrowser && (request.BrowserExpectedSecretVersion != "" || request.BrowserExpectedStorageStateSHA256 != "") {
		return &ValidationError{Field: "browser session metadata", Rule: "must be omitted for preview readiness"}
	}

	if err := validateDiagnosticsPath(request.DiagnosticsPath); err != nil {
		return err
	}
	return nil
}

func validJobForKind(job string, kind Kind) bool {
	switch kind {
	case KindBackend:
		matches := serviceJobPattern.FindStringSubmatch(job)
		return len(matches) == 2 && matches[1] == string(KindBackend)
	case KindOCR:
		matches := serviceJobPattern.FindStringSubmatch(job)
		return len(matches) == 2 && matches[1] == string(KindOCR)
	case KindBrowser:
		return previewBrowserJob.MatchString(job) || productionBrowserJob.MatchString(job)
	default:
		return false
	}
}

func validateDiagnosticsPath(path string) error {
	if path == "" || strings.ContainsAny(path, "\r\n") || filepath.Ext(path) != ".log" {
		return &ValidationError{Field: "DIAGNOSTICS_FILE", Rule: "must be a single-line .log path"}
	}
	directory := filepath.Dir(path)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return &ValidationError{Field: "DIAGNOSTICS_FILE", Rule: "directory must already exist"}
	}
	info, err = os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return &ValidationError{Field: "DIAGNOSTICS_FILE", Rule: "must not be a symbolic link"}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &ValidationError{Field: "DIAGNOSTICS_FILE", Rule: "must be inspectable"}
	}
	if err == nil && !info.Mode().IsRegular() {
		return &ValidationError{Field: "DIAGNOSTICS_FILE", Rule: "must be a regular file"}
	}
	return nil
}
