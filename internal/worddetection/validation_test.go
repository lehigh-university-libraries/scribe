package worddetection

import (
	"math"
	"strings"
	"testing"
)

func TestValidateBoxesRejectsHostileProviderOutput(t *testing.T) {
	t.Parallel()

	valid := WordBox{X: 10, Y: 20, Width: 30, Height: 40, Confidence: 0.9}
	tests := []struct {
		name  string
		words []WordBox
		max   int
		want  string
	}{
		{name: "too many", words: []WordBox{valid, valid}, max: 1, want: "maximum"},
		{name: "negative origin", words: []WordBox{{X: -1, Y: 0, Width: 1, Height: 1}}, max: 1, want: "outside"},
		{name: "zero width", words: []WordBox{{X: 0, Y: 0, Height: 1}}, max: 1, want: "outside"},
		{name: "right overflow", words: []WordBox{{X: 90, Y: 0, Width: 11, Height: 1}}, max: 1, want: "outside"},
		{name: "bottom overflow", words: []WordBox{{X: 0, Y: 90, Width: 1, Height: 11}}, max: 1, want: "outside"},
		{name: "nan confidence", words: []WordBox{{X: 0, Y: 0, Width: 1, Height: 1, Confidence: math.NaN()}}, max: 1, want: "finite"},
		{name: "infinite confidence", words: []WordBox{{X: 0, Y: 0, Width: 1, Height: 1, Confidence: math.Inf(1)}}, max: 1, want: "finite"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateBoxes(test.words, 100, 100, test.max)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateBoxes() error = %v; want containing %q", err, test.want)
			}
		})
	}
	if err := ValidateBoxes([]WordBox{valid}, 100, 100, 1); err != nil {
		t.Fatalf("ValidateBoxes(valid) error = %v", err)
	}
}
