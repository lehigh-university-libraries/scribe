package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// logImageConversionFailure records categorical diagnostics without formatting
// errors from Triplet, the filesystem, decoder, or ImageMagick. Those
// errors can contain response bodies, authenticated URLs, document content, or
// temporary paths.
func logImageConversionFailure(message string, err error, attrs ...any) {
	category := "internal"
	switch {
	case errors.Is(err, context.Canceled):
		category = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		category = "timeout"
	}
	attrs = append(attrs,
		"category", category,
		"error_type", fmt.Sprintf("%T", err),
	)
	slog.Warn(message, attrs...)
}
