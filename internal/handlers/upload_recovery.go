package handlers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const UploadTempRecoveryAge = time.Hour

// CleanupStaleUploadTemps removes only atomic-write temporaries created by
// Scribe and old enough that no live request can still own them. Symlinks,
// directories, canonical uploads, and recent temporaries are never touched.
func CleanupStaleUploadTemps(uploadDir string, now time.Time, minimumAge time.Duration) error {
	if strings.TrimSpace(uploadDir) == "" || minimumAge <= 0 {
		return fmt.Errorf("upload temporary cleanup requires a directory and positive age")
	}
	entries, err := os.ReadDir(uploadDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read upload directory: %w", err)
	}
	cutoff := now.Add(-minimumAge)
	var cleanupErrors []error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".scribe-upload-") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			if !errors.Is(infoErr, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, infoErr)
			}
			continue
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(uploadDir, entry.Name())
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, removeErr)
		}
	}
	return errors.Join(cleanupErrors...)
}
