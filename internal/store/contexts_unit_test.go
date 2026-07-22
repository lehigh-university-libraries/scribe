package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeContextMetadataBoundsAndFormatsScalarsOnce(t *testing.T) {
	t.Parallel()
	metadata, err := normalizeContextMetadata(map[string]any{
		"string": "manifest",
		"number": json.Number("42.5"),
		"bool":   true,
		"null":   nil,
	})
	if err != nil {
		t.Fatalf("normalize bounded metadata: %v", err)
	}
	want := map[string]string{"string": "manifest", "number": "42.5", "bool": "true", "null": "null"}
	for field, value := range want {
		if metadata[field] != value {
			t.Errorf("metadata[%q] = %q, want %q", field, metadata[field], value)
		}
	}
	if !matchesAll([]RuleCondition{
		{Field: "string", Operator: "starts_with", Value: "mani"},
		{Field: "number", Operator: "eq", Value: "42.5"},
	}, metadata) {
		t.Fatal("bounded normalized metadata did not match expected rule")
	}

	tooMany := make(map[string]any, MaxContextMetadataFields+1)
	for index := 0; index <= MaxContextMetadataFields; index++ {
		tooMany[fmt.Sprintf("field-%d", index)] = "value"
	}
	for name, candidate := range map[string]map[string]any{
		"too many fields": tooMany,
		"nested object":   {"nested": map[string]any{"key": "value"}},
		"nested array":    {"nested": []any{"value"}},
		"long key":        {strings.Repeat("k", MaxContextMetadataKeyBytes+1): "value"},
		"long scalar":     {"value": strings.Repeat("v", MaxContextMetadataScalarBytes+1)},
		"invalid number":  {"value": json.Number("not-a-number")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := normalizeContextMetadata(candidate); !errors.Is(err, ErrInvalidContextMetadata) {
				t.Fatalf("normalize error = %v, want ErrInvalidContextMetadata", err)
			}
		})
	}
}

func TestSelectionRuleValidationBoundsEvaluationWork(t *testing.T) {
	t.Parallel()
	valid := ContextSelectionRule{
		ContextID:  1,
		Conditions: []RuleCondition{{Field: "language", Operator: "eq", Value: "en"}},
	}
	if err := validateSelectionRule(valid); err != nil {
		t.Fatalf("bounded rule rejected: %v", err)
	}
	tooMany := valid
	tooMany.Conditions = make([]RuleCondition, MaxSelectionRuleConditions+1)
	if err := validateSelectionRule(tooMany); err == nil {
		t.Fatal("rule with too many conditions was accepted")
	}
	invalidOperator := valid
	invalidOperator.Conditions = []RuleCondition{{Field: "language", Operator: "regex", Value: ".*"}}
	if err := validateSelectionRule(invalidOperator); err == nil {
		t.Fatal("rule with unbounded operator was accepted")
	}
}
