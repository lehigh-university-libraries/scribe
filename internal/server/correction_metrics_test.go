package server

import (
	"testing"

	htrmetrics "github.com/lehigh-university-libraries/htr/pkg/metrics"
)

func TestCorrectionMetricUsesUnicodeHTRDistanceAfterStableNormalization(t *testing.T) {
	original := normalizeCorrectionMetricText("  CAFÉ\n世界  ")
	corrected := normalizeCorrectionMetricText("cafe 世界")
	if original != "café 世界" || corrected != "cafe 世界" {
		t.Fatalf("normalized values = %q, %q", original, corrected)
	}
	if distance := htrmetrics.LevenshteinDistance(original, corrected); distance != 1 {
		t.Fatalf("Unicode distance = %d, want 1", distance)
	}
}
