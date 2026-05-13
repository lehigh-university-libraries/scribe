package handlers

import "testing"

func TestSafeSessionNameSanitizesUploadAndURLNames(t *testing.T) {
	tests := map[string]string{
		"../../etc/passwd":        "passwd",
		"Page One.JPG":            "page-one",
		"weird/<script>.png":      "script",
		"https://example/x/a.jp2": "a",
		"":                        "workspace",
	}

	for raw, want := range tests {
		t.Run(raw, func(t *testing.T) {
			if got := safeSessionName(raw); got != want {
				t.Fatalf("safeSessionName(%q) = %q, want %q", raw, got, want)
			}
		})
	}
}
