// Package uploadref defines the application-relative identity of a stored
// upload without depending on a particular local or shared blob backend.
package uploadref

import (
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var immutableUploadNamePattern = regexp.MustCompile(`^[0-9a-f]{64}-[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.(?:gif|jp2|jpg|png|tiff|webp)$`)

// NameFromURL recognizes only application-relative Scribe upload URLs. An
// imported manifest may reference another origin with the same path and must
// never gain authority to delete that origin or a local Scribe object.
func NameFromURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	cleanPath := path.Clean(parsed.Path)
	if cleanPath != parsed.Path || path.Dir(cleanPath) != "/static/uploads" {
		return "", false
	}
	name := path.Base(cleanPath)
	if name == "." || name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	return name, true
}

// IsImmutableName reports whether name is a Scribe-produced content-hash plus
// UUID identity with one of the canonical image extensions. Parsing a local
// URL and authorizing a new stored object are deliberately separate: callers
// may still recognize an old/noncanonical URL without being allowed to create
// or stage it.
func IsImmutableName(name string) bool {
	return immutableUploadNamePattern.MatchString(strings.TrimSpace(name))
}

// ImmutableNameFromURL recognizes only producer-authored immutable upload
// identities. Write, stage, and canonical-reference boundaries use this API.
func ImmutableNameFromURL(raw string) (string, bool) {
	name, ok := NameFromURL(raw)
	if !ok || !IsImmutableName(name) {
		return "", false
	}
	return name, true
}
