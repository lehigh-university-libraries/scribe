package cloudrunreadiness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDiagnosticsAtomicallyCreatesPrivateRegularFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "diagnostics.log")
	diagnostics := Diagnostics{lines: []string{"typed-line"}}
	if err := writeDiagnostics(path, diagnostics); err != nil {
		t.Fatalf("writeDiagnostics: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want regular 0600", info.Mode())
	}
}

func TestWriteDiagnosticsRejectsTargetSymlinkAtWriteBoundary(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sensitive := filepath.Join(directory, "sensitive")
	if err := os.WriteFile(sensitive, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	path := filepath.Join(directory, "diagnostics.log")
	if err := os.Symlink(sensitive, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := writeDiagnostics(path, Diagnostics{lines: []string{"replacement"}}); err == nil {
		t.Fatal("writeDiagnostics accepted a symlink target")
	}
	data, err := os.ReadFile(sensitive)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "preserve" {
		t.Fatalf("sensitive target = %q, want preserved", data)
	}
}
