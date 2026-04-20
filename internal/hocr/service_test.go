package hocr

import "testing"

func TestParseSegmentationModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantKind  string
		wantModel string
	}{
		{name: "empty defaults to auto", input: "", wantKind: "auto", wantModel: ""},
		{name: "auto", input: "auto", wantKind: "auto", wantModel: ""},
		{name: "tesseract", input: "tesseract", wantKind: "tesseract", wantModel: ""},
		{name: "scribe", input: "scribe", wantKind: "scribe", wantModel: ""},
		{name: "kraken shorthand", input: "kraken", wantKind: "kraken", wantModel: ""},
		{name: "kraken explicit model", input: "kraken:blla.mlmodel", wantKind: "kraken", wantModel: "blla.mlmodel"},
		{name: "kraken preserves model case", input: "KRAKEN:Models/Latin.mlmodel", wantKind: "kraken", wantModel: "Models/Latin.mlmodel"},
		{name: "unknown falls back to auto", input: "something-else", wantKind: "auto", wantModel: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotKind, gotModel := parseSegmentationModel(tt.input)
			if gotKind != tt.wantKind || gotModel != tt.wantModel {
				t.Fatalf("parseSegmentationModel(%q) = (%q, %q), want (%q, %q)", tt.input, gotKind, gotModel, tt.wantKind, tt.wantModel)
			}
		})
	}
}
