package safefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileLimitRejectsOversizedContentBeforeReturningBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider-output.bin")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := ReadFileLimit(path, 4); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("ReadFileLimit error = %v, want ErrFileTooLarge", err)
	}
	data, err := ReadFileLimit(path, 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("ReadFileLimit exact boundary = %q, %v", data, err)
	}
}

func TestReadFileLimitRejectsNegativeLimit(t *testing.T) {
	if _, err := ReadFileLimit("unused", -1); err == nil {
		t.Fatal("ReadFileLimit accepted a negative limit")
	}
}
