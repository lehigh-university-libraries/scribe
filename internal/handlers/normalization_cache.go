package handlers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

var normalizationCacheMu sync.Mutex

type normalizationCacheEntry struct {
	path    string
	size    uint64
	modTime time.Time
}

func writeNormalizationCache(cacheDir, cachePath string, data []byte) error {
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return fmt.Errorf("create normalization cache: %w", err)
	}
	temporary, err := os.CreateTemp(cacheDir, ".scribe-normalization-*")
	if err != nil {
		return fmt.Errorf("create normalization cache temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, cachePath); err != nil { // #nosec G703 -- cachePath is a server-derived SHA-256 filename.
		return err
	}
	return pruneConfiguredNormalizationCache(cacheDir)
}

func touchNormalizationCache(cachePath string) {
	now := time.Now().UTC()
	_ = os.Chtimes(cachePath, now, now)
}

func pruneConfiguredNormalizationCache(cacheDir string) error {
	storage := config.Get().Config.Storage
	if storage.NormalizationCacheMaxBytes == 0 {
		storage.NormalizationCacheMaxBytes = config.DefaultNormalizationCacheMaxBytes
	}
	if storage.NormalizationCacheMaxAge == 0 {
		storage.NormalizationCacheMaxAge = config.DefaultNormalizationCacheMaxAge
	}
	return pruneNormalizationCache(cacheDir, storage.NormalizationCacheMaxBytes, storage.NormalizationCacheMaxAge, time.Now().UTC())
}

func pruneNormalizationCache(cacheDir string, maxBytes uint64, maxAge time.Duration, now time.Time) error {
	normalizationCacheMu.Lock()
	defer normalizationCacheMu.Unlock()

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	cutoff := now.Add(-maxAge)
	files := make([]normalizationCacheEntry, 0, len(entries))
	var total uint64
	var cleanupErrors []error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, "_converted.jpg") && !strings.HasPrefix(name, ".scribe-normalization-") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			if !errors.Is(infoErr, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, infoErr)
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Size() < 0 {
			continue
		}
		path := filepath.Join(cacheDir, name)
		if strings.HasPrefix(name, ".scribe-normalization-") {
			if info.ModTime().Before(now.Add(-time.Hour)) {
				if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					cleanupErrors = append(cleanupErrors, removeErr)
				}
			}
			continue
		}
		removeForAge := maxAge > 0 && info.ModTime().Before(cutoff)
		if removeForAge {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, removeErr)
			}
			continue
		}
		size := uint64(info.Size()) // #nosec G115 -- negative file sizes are rejected above.
		if total > ^uint64(0)-size {
			total = ^uint64(0)
		} else {
			total += size
		}
		files = append(files, normalizationCacheEntry{path: path, size: size, modTime: info.ModTime()})
	}
	if total > maxBytes {
		sort.Slice(files, func(i, j int) bool {
			if files[i].modTime.Equal(files[j].modTime) {
				return files[i].path < files[j].path
			}
			return files[i].modTime.Before(files[j].modTime)
		})
		for _, file := range files {
			if total <= maxBytes {
				break
			}
			if removeErr := os.Remove(file.path); removeErr != nil {
				if !errors.Is(removeErr, os.ErrNotExist) {
					cleanupErrors = append(cleanupErrors, removeErr)
				}
				continue
			}
			if total >= file.size {
				total -= file.size
			} else {
				total = 0
			}
		}
	}
	return errors.Join(cleanupErrors...)
}
