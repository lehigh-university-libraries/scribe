package iiif

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// MaxJSONNestingDepth bounds recursive extension data before schema walkers,
// normalizers, and browser clients process it. Presentation resources do not
// need attacker-controlled thousands-deep arrays or objects.
const MaxJSONNestingDepth = 128

// DecodeJSON decodes one JSON value without coercing extension numbers to
// float64. IIIF resources are extensible JSON-LD documents, so values outside
// Scribe's vocabulary must survive normalization and editor round trips byte
// for value, including integers larger than JavaScript's safe integer range.
func DecodeJSON(raw []byte, destination any) error {
	if err := validateJSONNesting(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return fmt.Errorf("decode trailing JSON data: %w", err)
	}
	return nil
}

func validateJSONNesting(raw []byte) error {
	depth := 0
	inString := false
	escaped := false
	for _, character := range raw {
		if inString {
			switch {
			case escaped:
				escaped = false
			case character == '\\':
				escaped = true
			case character == '"':
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > MaxJSONNestingDepth {
				return fmt.Errorf("json nesting exceeds %d levels", MaxJSONNestingDepth)
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}
