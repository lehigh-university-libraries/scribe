package iiif

import (
	"strings"
	"testing"
)

func TestDecodeJSONBoundsNestingBeforeSchemaTraversal(t *testing.T) {
	t.Parallel()
	withinLimit := strings.Repeat("[", MaxJSONNestingDepth) + "0" + strings.Repeat("]", MaxJSONNestingDepth)
	var decoded any
	if err := DecodeJSON([]byte(withinLimit), &decoded); err != nil {
		t.Fatalf("boundary-depth JSON rejected: %v", err)
	}

	overLimit := "[" + withinLimit + "]"
	if err := DecodeJSON([]byte(overLimit), &decoded); err == nil || !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("over-depth JSON error = %v", err)
	}
}

func TestDecodeJSONNestingScannerIgnoresEscapedStringContent(t *testing.T) {
	t.Parallel()
	raw := `{"value":"[\\\"{{[]}}\\\"]","items":[{"ok":true}]}`
	var decoded map[string]any
	if err := DecodeJSON([]byte(raw), &decoded); err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}
	if decoded["value"] != `[\"{{[]}}\"]` {
		t.Fatalf("decoded string = %#v", decoded["value"])
	}
}
