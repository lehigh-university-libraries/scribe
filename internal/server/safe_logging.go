package server

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/lehigh-university-libraries/scribe/internal/safelog"
)

func safeLogErrorType(err error) string {
	return safelog.ErrorType(err)
}

func safeLogErrorCategory(err error) string {
	return safelog.ErrorCategory(err)
}

// safeLogValueID permits correlation without putting a URL, path, provider
// payload, or other attacker-controlled value in operational logs.
func safeLogValueID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}
