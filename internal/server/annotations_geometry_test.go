package server

import (
	"fmt"
	"math"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func TestParseXYWHRejectsNonFiniteNegativeAndOverflowGeometry(t *testing.T) {
	t.Parallel()

	invalid := []string{
		"NaN,0,1,1",
		"+Inf,0,1,1",
		"-1,0,1,1",
		"0,0,0,1",
		fmt.Sprintf("%d,0,2,1", iiif.MaxPixelCoordinate),
		fmt.Sprintf("%g,0,1,1", math.MaxFloat64),
	}
	for _, fragment := range invalid {
		if _, _, _, _, err := parseXYWH(fragment); err == nil {
			t.Errorf("parseXYWH(%q) succeeded", fragment)
		}
		if rounded := roundXYWHFragment(fragment); rounded != fragment {
			t.Errorf("roundXYWHFragment(%q) = %q; invalid geometry should remain rejectable", fragment, rounded)
		}
	}

	x1, y1, x2, y2, err := parseXYWH("1.4,2.5,3.6,4.4")
	if err != nil || x1 != 1 || y1 != 3 || x2 != 5 || y2 != 7 {
		t.Fatalf("parseXYWH(decimal) = %d,%d,%d,%d/%v", x1, y1, x2, y2, err)
	}
}
