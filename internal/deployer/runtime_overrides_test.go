package deployer

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestWriteRuntimeOverrides(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"IIIF_MAX_MANIFEST_CANVASES":                  "300",
		"STORAGE_RESERVATION_TTL":                     "45m",
		"TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE": "",
	}
	var output bytes.Buffer
	if err := WriteRuntimeOverrides(&output, func(name string) string { return values[name] }); err != nil {
		t.Fatalf("WriteRuntimeOverrides returned error: %v", err)
	}
	want := "TF_VAR_iiif_max_manifest_canvases=300\nTF_VAR_storage_reservation_ttl=45m\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestWriteRuntimeOverridesRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		environment string
		value       string
	}{
		{name: "newline", environment: "IIIF_MAX_MANIFEST_IMPORT_BYTES", value: "1\nTF_VAR_project_id=attacker"},
		{name: "non-number", environment: "IIIF_MAX_MANIFEST_IMPORT_BYTES", value: "many"},
		{name: "below minimum", environment: "IIIF_MAX_MANIFEST_IMPORT_BYTES", value: "0"},
		{name: "above maximum", environment: "IIIF_MAX_MANIFEST_IMPORT_BYTES", value: "67108865"},
		{name: "fraction", environment: "IIIF_MAX_MANIFEST_CANVASES", value: "1.5"},
		{name: "short reservation", environment: "STORAGE_RESERVATION_TTL", value: "299s"},
		{name: "long reservation", environment: "STORAGE_RESERVATION_TTL", value: "25h"},
		{name: "short cache age", environment: "STORAGE_NORMALIZATION_CACHE_MAX_AGE", value: "59m"},
		{name: "long cache age", environment: "STORAGE_NORMALIZATION_CACHE_MAX_AGE", value: "8761h"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			err := WriteRuntimeOverrides(&output, func(name string) string {
				if name == test.environment {
					return test.value
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), test.environment) {
				t.Fatalf("error = %v, want named validation error", err)
			}
			if output.Len() != 0 {
				t.Fatalf("output = %q, want empty", output.String())
			}
		})
	}
}

func TestWriteRuntimeOverridesReportsWriteFailure(t *testing.T) {
	t.Parallel()

	err := WriteRuntimeOverrides(errorWriter{}, func(name string) string {
		if name == "IIIF_MAX_MANIFEST_CANVASES" {
			return "1"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "write runtime override") {
		t.Fatalf("error = %v, want write failure", err)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
