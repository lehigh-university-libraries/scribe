// Package providersecret owns the tenant boundary for provider credentials.
package providersecret

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

const DefaultVaultPrefix = "scribe/dev/provider-secrets/workspaces"

// VaultPrefix returns the normalized configured prefix or the safe default.
func VaultPrefix(configured string) string {
	prefix := strings.Trim(strings.TrimSpace(configured), "/")
	if prefix == "" {
		return DefaultVaultPrefix
	}
	return prefix
}

// ValidateVaultPath requires secret material to be a strict descendant of the
// authenticated workspace's provider-secret prefix. The path comes from
// persistence, but it is still treated as untrusted before every Vault call.
func ValidateVaultPath(configuredPrefix string, workspaceID uint64, rawPath string) error {
	if workspaceID == 0 {
		return fmt.Errorf("provider secret workspace is required")
	}
	path := strings.TrimSpace(rawPath)
	if path == "" || path != rawPath {
		return fmt.Errorf("provider secret vault path is invalid")
	}
	prefix := VaultPrefix(configuredPrefix)
	if err := validateSegments(prefix); err != nil {
		return fmt.Errorf("provider secret vault prefix is invalid: %w", err)
	}
	if err := validateSegments(path); err != nil {
		return fmt.Errorf("provider secret vault path is invalid: %w", err)
	}
	expected := prefix + "/" + strconv.FormatUint(workspaceID, 10) + "/"
	if !strings.HasPrefix(path, expected) || len(path) == len(expected) {
		return fmt.Errorf("provider secret vault path is outside workspace scope")
	}
	return nil
}

func validateSegments(path string) error {
	if strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return fmt.Errorf("empty path segment")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, `\`) {
			return fmt.Errorf("unsafe path segment")
		}
		for _, r := range segment {
			if unicode.IsControl(r) || unicode.IsSpace(r) {
				return fmt.Errorf("unsafe path character")
			}
		}
	}
	return nil
}
