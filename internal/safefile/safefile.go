package safefile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrFileTooLarge = errors.New("file exceeds byte limit")

func CleanPath(path string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || clean == "" {
		return "", fmt.Errorf("file path is required")
	}
	return clean, nil
}

func Open(path string) (*os.File, error) {
	clean, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	return os.Open(clean) // #nosec G304 -- path is normalized here; callers pass local pipeline paths, not request path components.
}

// ReadFileLimit reads a trusted local pipeline path without allowing a
// corrupted cache entry or runaway subprocess output to allocate unbounded
// memory. The post-read check also covers a file that grows after Stat.
func ReadFileLimit(path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("file byte limit cannot be negative")
	}
	file, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr != nil {
		return nil, statErr
	} else if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: size %d exceeds %d", ErrFileTooLarge, info.Size(), maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: content exceeds %d", ErrFileTooLarge, maxBytes)
	}
	return data, nil
}
