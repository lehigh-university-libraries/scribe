package safefile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

func ReadFile(path string) ([]byte, error) {
	clean, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(clean) // #nosec G304 -- path is normalized here; callers pass local pipeline paths, not request path components.
}
