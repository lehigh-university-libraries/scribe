package handlers

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNormalizationCachePrunesByAgeAndLRUSize(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	write := func(name string, size int, age time.Duration) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, size), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(-age)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return path
	}
	aged := write("aged_converted.jpg", 6, 48*time.Hour)
	oldest := write("oldest_converted.jpg", 6, 3*time.Hour)
	middle := write("middle_converted.jpg", 6, 2*time.Hour)
	newest := write("newest_converted.jpg", 6, time.Hour)
	unknown := write("canonical-upload.png", 100, 72*time.Hour)
	staleTemporary := write(".scribe-normalization-crashed", 100, 2*time.Hour)

	if err := pruneNormalizationCache(dir, 10, 24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{aged, oldest, middle, staleTemporary} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("pruned file %q still exists: %v", removed, err)
		}
	}
	for _, retained := range []string{newest, unknown} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("retained file %q: %v", retained, err)
		}
	}
}

func TestNormalizationCacheWritesAreAtomicAndBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "digest_converted.jpg")
	payloads := [][]byte{bytes.Repeat([]byte{'a'}, 1024), bytes.Repeat([]byte{'b'}, 2048)}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, payload := range payloads {
		payload := payload
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if err := writeNormalizationCache(dir, path, payload); err != nil {
				t.Errorf("writeNormalizationCache: %v", err)
			}
		}()
	}
	close(start)
	workers.Wait()

	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, payloads[0]) && !bytes.Equal(stored, payloads[1]) {
		t.Fatalf("cache contains a partial concurrent write (%d bytes)", len(stored))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %o, want 600", info.Mode().Perm())
	}
	if err := pruneNormalizationCache(dir, 1, 24*time.Hour, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversized cache entry still exists: %v", err)
	}
}
