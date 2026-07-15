//go:build !remoteocr

package worddetection

import (
	"os"
	"path/filepath"
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
