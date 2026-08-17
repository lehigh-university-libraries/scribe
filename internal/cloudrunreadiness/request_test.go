package cloudrunreadiness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRequestRejectsNoncanonicalProductionSecretVersion(t *testing.T) {
	t.Parallel()
	diagnostics := filepath.Join(t.TempDir(), "browser.log")
	environment := map[string]string{
		projectEnvironment:              "scribe-test",
		regionEnvironment:               "us-central1",
		browserSecretVersionEnvironment: "02",
		browserStateDigestEnvironment:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	_, err := ParseRequest([]string{"scribe-browser-deadbeef", "browser", diagnostics}, func(key string) string {
		return environment[key]
	})
	if !IsValidationError(err) {
		t.Fatalf("error = %v, want validation error", err)
	}
}

func TestParseRequestAcceptsExactProductionBrowserMetadata(t *testing.T) {
	t.Parallel()
	diagnostics := filepath.Join(t.TempDir(), "browser.log")
	environment := baseRequestEnvironment()
	environment[browserSecretVersionEnvironment] = "2"
	environment[browserStateDigestEnvironment] = strings.Repeat("a", 64)
	request, err := ParseRequest([]string{"scribe-browser-deadbeef", "browser", diagnostics}, mapGetenv(environment))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if request.BrowserExpectedSecretVersion != "2" || request.BrowserExpectedStorageStateSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("browser metadata = %q %q", request.BrowserExpectedSecretVersion, request.BrowserExpectedStorageStateSHA256)
	}
}

func TestParseRequestRejectsMissingOrInvalidProductionBrowserMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version string
		digest  string
	}{
		{name: "missing version", digest: strings.Repeat("a", 64)},
		{name: "version one", version: "1", digest: strings.Repeat("a", 64)},
		{name: "missing digest", version: "2"},
		{name: "uppercase digest", version: "2", digest: strings.Repeat("A", 64)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := baseRequestEnvironment()
			environment[browserSecretVersionEnvironment] = test.version
			environment[browserStateDigestEnvironment] = test.digest
			_, err := ParseRequest([]string{
				"scribe-browser-deadbeef",
				"browser",
				filepath.Join(t.TempDir(), "browser.log"),
			}, mapGetenv(environment))
			if !IsValidationError(err) {
				t.Fatalf("error = %v, want validation error", err)
			}
		})
	}
}

func TestParseRequestRejectsProductionMetadataForPreviewBrowser(t *testing.T) {
	t.Parallel()
	for _, key := range []string{browserSecretVersionEnvironment, browserStateDigestEnvironment} {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			environment := baseRequestEnvironment()
			environment[key] = "2"
			_, err := ParseRequest([]string{
				"scribe-pr-7-browser-deadbeef",
				"browser",
				filepath.Join(t.TempDir(), "browser.log"),
			}, mapGetenv(environment))
			if !IsValidationError(err) {
				t.Fatalf("error = %v, want validation error", err)
			}
		})
	}
}

func TestParseRequestRejectsInvalidOrMismatchedJobs(t *testing.T) {
	t.Parallel()
	tests := [][]string{
		{"scribe-prod-ocr-readiness", "backend"},
		{"scribe-prod-backend-readiness", "ocr"},
		{"scribe-browser-deadbeef", "backend"},
		{"arbitrary-readiness", "browser"},
	}
	for _, arguments := range tests {
		arguments := arguments
		t.Run(strings.Join(arguments, "-"), func(t *testing.T) {
			t.Parallel()
			_, err := ParseRequest([]string{arguments[0], arguments[1], filepath.Join(t.TempDir(), "diagnostics.log")}, mapGetenv(baseRequestEnvironment()))
			if !IsValidationError(err) {
				t.Fatalf("error = %v, want validation error", err)
			}
		})
	}
}

func TestParseRequestRejectsUnsafeDiagnosticsTargets(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sensitive := filepath.Join(directory, "sensitive")
	if err := os.WriteFile(sensitive, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	symlink := filepath.Join(directory, "symlink.log")
	if err := os.Symlink(sensitive, symlink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	nonregular := filepath.Join(directory, "directory.log")
	if err := os.Mkdir(nonregular, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	tests := []string{
		filepath.Join(directory, "newline.log") + "\nsecret",
		filepath.Join(directory, "missing", "diagnostics.log"),
		symlink,
		nonregular,
		filepath.Join(directory, "not-a-log.txt"),
	}
	for index, path := range tests {
		_, err := ParseRequest([]string{"scribe-prod-backend-readiness", "backend", path}, mapGetenv(baseRequestEnvironment()))
		if !IsValidationError(err) {
			t.Errorf("case %d error = %v, want validation error", index, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("case %d disclosed rejected path: %v", index, err)
		}
	}
}

func baseRequestEnvironment() map[string]string {
	return map[string]string{
		projectEnvironment: "scribe-test",
		regionEnvironment:  "us-central1",
	}
}

func mapGetenv(environment map[string]string) func(string) string {
	return func(key string) string { return environment[key] }
}
