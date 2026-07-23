//go:build !remoteocr

package hocr

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

func TestPersistTemporaryImageRemovesFileAfterWriteFailure(t *testing.T) {
	var createdPath string
	_, err := persistTemporaryImage("scribe-write-failure-*.png", []byte("private image bytes"), func(path string, _ []byte, _ fs.FileMode) error {
		createdPath = path
		return errors.New("injected write failure")
	})
	if err == nil || createdPath == "" {
		t.Fatalf("persistTemporaryImage error/path = %v/%q, want injected failure and path", err, createdPath)
	}
	if _, statErr := os.Stat(createdPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary image after write failure stat error = %v, want not exist", statErr)
	}
}
