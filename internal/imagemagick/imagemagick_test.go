package imagemagick

import (
	"slices"
	"testing"
)

func TestLimitedArgumentsPrependResourcePolicy(t *testing.T) {
	input := []string{"input.jp2", "output.jpg"}
	got := limitedArguments(input)
	if !slices.Equal(got[:len(resourceLimits)], resourceLimits) {
		t.Fatalf("resource prefix = %q; want %q", got[:len(resourceLimits)], resourceLimits)
	}
	if !slices.Equal(got[len(resourceLimits):], input) {
		t.Fatalf("operation args = %q; want %q", got[len(resourceLimits):], input)
	}
	got[0] = "changed"
	if resourceLimits[0] != "-limit" {
		t.Fatal("limitedArguments aliased the global resource policy")
	}
}
