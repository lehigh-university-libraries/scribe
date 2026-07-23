package safelog

import (
	"context"
	"strings"
	"testing"
)

type secretBearingError struct{}

func (secretBearingError) Error() string {
	panic("ErrorType must not render an untrusted error")
}

func TestErrorTypeDoesNotRenderErrorText(t *testing.T) {
	got := ErrorType(secretBearingError{})
	if !strings.Contains(got, "secretBearingError") {
		t.Fatalf("ErrorType() = %q", got)
	}
}

func TestErrorCategoryExposesOnlyBoundedCategories(t *testing.T) {
	if got := ErrorCategory(context.Canceled); got != "canceled" {
		t.Fatalf("canceled category = %q", got)
	}
	if got := ErrorCategory(context.DeadlineExceeded); got != "timeout" {
		t.Fatalf("deadline category = %q", got)
	}
	if got := ErrorCategory(secretBearingError{}); got != "failed" {
		t.Fatalf("generic category = %q", got)
	}
}
