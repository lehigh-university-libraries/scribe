//go:build !remoteocr

package worddetection

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseKrakenJSONUsesBaselineFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken.json")
	if err := os.WriteFile(path, []byte(`{
		"image_size": [300, 200],
		"lines": [
			{"baseline": [[10, 42], [110, 42]], "boundary": []}
		]
	}`), 0o600); err != nil {
		t.Fatalf("write kraken fixture: %v", err)
	}

	boxes, err := parseKrakenJSON(path)
	if err != nil {
		t.Fatalf("parseKrakenJSON() error = %v", err)
	}
	if len(boxes) != 1 {
		t.Fatalf("parseKrakenJSON() returned %d boxes, want 1", len(boxes))
	}
	got := boxes[0]
	want := WordBox{X: 10, Y: 32, Width: 100, Height: 20}
	if got != want {
		t.Fatalf("baseline fallback box = %+v, want %+v", got, want)
	}
}

func TestKrakenFailureRedactsModelPathsAndProcessOutput(t *testing.T) {
	binDir := t.TempDir()
	krakenPath := filepath.Join(binDir, "kraken")
	const privateDiagnostic = "PRIVATE_MANUSCRIPT_TEXT_AND_TOKEN"
	if err := os.WriteFile(krakenPath, []byte("#!/bin/sh\necho '"+privateDiagnostic+"' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write fake kraken: %v", err)
	}
	t.Setenv("PATH", binDir)
	const privateModelPath = "/private/models/customer-secret.mlmodel"
	const privateImagePath = "/private/uploads/manuscript-secret.png"
	provider := NewKraken(privateModelPath)
	_, err := provider.DetectWords(context.Background(), privateImagePath)
	if err == nil || err.Error() != "kraken segmentation failed" {
		t.Fatalf("DetectWords error = %v, want categorical failure", err)
	}
	for _, secret := range []string{privateDiagnostic, privateModelPath, privateImagePath, "customer-secret"} {
		if strings.Contains(err.Error(), secret) || strings.Contains(provider.Name(), secret) {
			t.Fatalf("Kraken failure/provider name exposed %q: %q / %q", secret, err, provider.Name())
		}
	}
}
