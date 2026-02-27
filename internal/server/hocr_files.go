package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var sessionIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeSessionID(sessionID string) string {
	clean := strings.TrimSpace(sessionID)
	if clean == "" {
		return "unknown"
	}
	return sessionIDSanitizer.ReplaceAllString(clean, "_")
}

func sessionHOCRDir(sessionID string) string {
	return filepath.Join("cache", "sessions", safeSessionID(sessionID))
}

func writeSessionHOCR(sessionID, filename, body string) error {
	dir := sessionHOCRDir(sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o644)
}

func readSessionHOCR(sessionID, filename string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(sessionHOCRDir(sessionID), filename))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", false
	}
	return s, true
}

