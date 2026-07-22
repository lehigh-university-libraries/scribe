package store

import (
	"strconv"
	"strings"
)

const maxDurableFailureMessageBytes = 128

type durableFailureOperation uint8

const (
	durableFailureExternalRequest durableFailureOperation = iota
	durableFailureResourceCleanup
	durableFailureAnnotationMirror
	durableFailureWebhook
)

type durableFailureCategory uint8

const (
	durableFailureGeneric durableFailureCategory = iota
	durableFailureInterrupted
	durableFailureAuthorization
	durableFailureLimit
	durableFailureValidation
	durableFailureDependency
)

// safeDurableFailureMessage is the sole persistence sanitizer for failures
// crossing an external trust boundary. Its output is always a fixed category;
// it never reflects provider bodies, request text, URLs, credentials, SQL
// diagnostics, or secret-store details supplied by a caller.
func safeDurableFailureMessage(operation durableFailureOperation, message string) string {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if operation == durableFailureWebhook {
		if status, ok := boundedHTTPStatus(normalized); ok {
			return "status " + strconv.Itoa(status)
		}
	}

	category := durableFailureGeneric
	switch {
	case strings.Contains(normalized, "context canceled"),
		strings.Contains(normalized, "deadline exceeded"),
		strings.Contains(normalized, "timed out"),
		strings.Contains(normalized, "timeout"),
		strings.Contains(normalized, "worker shutting down"):
		category = durableFailureInterrupted
	case strings.Contains(normalized, "unauthorized"),
		strings.Contains(normalized, "forbidden"),
		strings.Contains(normalized, "authentication"),
		strings.Contains(normalized, "credential"),
		strings.Contains(normalized, "permission denied"):
		category = durableFailureAuthorization
	case strings.Contains(normalized, "quota"),
		strings.Contains(normalized, "rate limit"),
		strings.Contains(normalized, "resource exhausted"),
		strings.Contains(normalized, "too large"),
		strings.Contains(normalized, "maximum"),
		strings.Contains(normalized, "configured limit"):
		category = durableFailureLimit
	case strings.Contains(normalized, "invalid"),
		strings.Contains(normalized, "malformed"),
		strings.Contains(normalized, "unsupported"),
		strings.Contains(normalized, "validation"),
		strings.Contains(normalized, "rejected"):
		category = durableFailureValidation
	case strings.Contains(normalized, "provider"),
		strings.Contains(normalized, "upstream"),
		strings.Contains(normalized, "http"),
		strings.Contains(normalized, "network"),
		strings.Contains(normalized, "connection"),
		strings.Contains(normalized, "unavailable"),
		strings.Contains(normalized, "dial"):
		category = durableFailureDependency
	}

	result := durableFailureCategoryMessage(operation, category)
	if len(result) > maxDurableFailureMessageBytes {
		return durableFailureCategoryMessage(operation, durableFailureGeneric)
	}
	return result
}

func durableFailureCategoryMessage(operation durableFailureOperation, category durableFailureCategory) string {
	switch operation {
	case durableFailureExternalRequest:
		switch category {
		case durableFailureInterrupted:
			return "external request was interrupted"
		case durableFailureAuthorization:
			return "external provider authentication failed"
		case durableFailureLimit:
			return "external request exceeded a configured limit"
		case durableFailureValidation:
			return "external request validation failed"
		case durableFailureDependency:
			return "external provider request failed"
		default:
			return "external request failed"
		}
	case durableFailureResourceCleanup:
		switch category {
		case durableFailureInterrupted:
			return "resource cleanup was interrupted"
		case durableFailureAuthorization:
			return "resource cleanup authorization failed"
		case durableFailureLimit:
			return "resource cleanup was rate limited"
		case durableFailureValidation:
			return "resource cleanup request was rejected"
		case durableFailureDependency:
			return "resource cleanup dependency failed"
		default:
			return "resource cleanup failed"
		}
	case durableFailureAnnotationMirror:
		switch category {
		case durableFailureInterrupted:
			return "annotation mirror delivery was interrupted"
		case durableFailureAuthorization:
			return "annotation mirror authorization failed"
		case durableFailureLimit:
			return "annotation mirror delivery was rate limited"
		case durableFailureValidation:
			return "annotation mirror payload was rejected"
		default:
			return "annotation mirror delivery failed"
		}
	case durableFailureWebhook:
		switch category {
		case durableFailureInterrupted:
			return "webhook request timed out"
		case durableFailureAuthorization:
			return "webhook request authorization failed"
		case durableFailureLimit:
			return "webhook request was rate limited"
		case durableFailureValidation:
			return "webhook request was rejected"
		default:
			return "webhook request failed"
		}
	default:
		return "external operation failed"
	}
}

func boundedHTTPStatus(message string) (int, bool) {
	fields := strings.Fields(message)
	if len(fields) != 2 || fields[0] != "status" || len(fields[1]) != 3 {
		return 0, false
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil || status < 100 || status > 599 {
		return 0, false
	}
	return status, true
}

// SafeResourceCleanupFailureMessage returns the same bounded category used by
// durable cleanup state, suitable for structured operational logs.
func SafeResourceCleanupFailureMessage(cause error) string {
	if cause == nil {
		return safeDurableFailureMessage(durableFailureResourceCleanup, "")
	}
	return safeDurableFailureMessage(durableFailureResourceCleanup, cause.Error())
}

// SafeAnnotationMirrorFailureMessage returns the same bounded category used by
// durable mirror state, suitable for structured operational logs.
func SafeAnnotationMirrorFailureMessage(cause error) string {
	if cause == nil {
		return safeDurableFailureMessage(durableFailureAnnotationMirror, "")
	}
	return safeDurableFailureMessage(durableFailureAnnotationMirror, cause.Error())
}
