// Package safelog provides non-reflecting error attributes for operational
// logs. Error strings may contain URLs, credentials, provider bodies, document
// text, SQL values, Vault paths, or temporary filenames and are never logged.
package safelog

import (
	"context"
	"errors"
	"fmt"
)

func ErrorType(err error) string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", err)
}

func ErrorCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "failed"
	}
}
