package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

// uniqueName returns a name that is unlikely to collide across concurrent test
// runs by appending the current UnixNano timestamp.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// TestContextResolution_DefaultWhenNoRules verifies that Resolve returns the
// default context and isDefault=true when no rules exist for the given
// metadata.
func TestContextResolution_DefaultWhenNoRules(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	s := store.NewContextStore(db)

	// Ensure there is exactly one default context for this test. Because
	// EnsureDefault is a no-op when a default already exists we create our own
	// via Create so we can clean it up deterministically.
	defaultCtx, err := s.Create(ctx, store.Context{
		Name:                  uniqueName("test-default-no-rules"),
		IsDefault:             true,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "test-model",
	})
	if err != nil {
		t.Fatalf("Create default context: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Delete(context.Background(), defaultCtx.ID); err != nil {
			t.Logf("cleanup: delete context %d: %v", defaultCtx.ID, err)
		}
	})

	resolved, isDefault, err := s.Resolve(ctx, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// isDefault should be true because no rule matched.
	if !isDefault {
		t.Errorf("isDefault = false; want true (no rules exist)")
	}
	// The resolved context must be the system default, not necessarily ours
	// (another default may already exist in the DB), but it must be marked as
	// default.
	if !resolved.IsDefault {
		t.Errorf("resolved.IsDefault = false; want true")
	}
	_ = resolved
}

// TestContextResolution_ExactMatchRule verifies that Resolve returns context B
// when a selection rule for B matches the supplied metadata.
func TestContextResolution_ExactMatchRule(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	s := store.NewContextStore(db)

	// Context A — will be the default.
	ctxA, err := s.Create(ctx, store.Context{
		Name:                  uniqueName("test-ctx-A"),
		IsDefault:             true,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "model-a",
	})
	if err != nil {
		t.Fatalf("Create context A: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Delete(context.Background(), ctxA.ID)
	})

	// Context B — targeted by the rule.
	ctxB, err := s.Create(ctx, store.Context{
		Name:                  uniqueName("test-ctx-B"),
		IsDefault:             false,
		SegmentationModel:     "kraken",
		TranscriptionProvider: "openai",
		TranscriptionModel:    "model-b",
	})
	if err != nil {
		t.Fatalf("Create context B: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Delete(context.Background(), ctxB.ID)
	})

	// Rule: if source_type == "manifest" → use context B.
	rule, err := s.CreateRule(ctx, store.ContextSelectionRule{
		ContextID: ctxB.ID,
		Priority:  10,
		Conditions: []store.RuleCondition{
			{Field: "source_type", Operator: "eq", Value: "manifest"},
		},
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	t.Cleanup(func() {
		_ = s.DeleteRule(context.Background(), rule.ID)
	})

	resolved, isDefault, err := s.Resolve(ctx, map[string]any{"source_type": "manifest"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if isDefault {
		t.Errorf("isDefault = true; want false (rule should have matched)")
	}
	if resolved.ID != ctxB.ID {
		t.Errorf("resolved.ID = %d; want %d (context B)", resolved.ID, ctxB.ID)
	}
	if resolved.TranscriptionModel != "model-b" {
		t.Errorf("resolved.TranscriptionModel = %q; want %q", resolved.TranscriptionModel, "model-b")
	}
}

// TestContextResolution_NoMatchFallsBackToDefault verifies that Resolve returns
// the default context when no rule conditions match the supplied metadata.
func TestContextResolution_NoMatchFallsBackToDefault(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	s := store.NewContextStore(db)

	// Default context.
	ctxDefault, err := s.Create(ctx, store.Context{
		Name:                  uniqueName("test-default-fallback"),
		IsDefault:             true,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "default-model",
	})
	if err != nil {
		t.Fatalf("Create default context: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Delete(context.Background(), ctxDefault.ID)
	})

	// A second context pointed to by a rule that won't fire.
	ctxOther, err := s.Create(ctx, store.Context{
		Name:                  uniqueName("test-other-fallback"),
		IsDefault:             false,
		SegmentationModel:     "kraken",
		TranscriptionProvider: "openai",
		TranscriptionModel:    "other-model",
	})
	if err != nil {
		t.Fatalf("Create other context: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Delete(context.Background(), ctxOther.ID)
	})

	// Rule matches source_type=="manifest" but we will call with source_type=="url".
	rule, err := s.CreateRule(ctx, store.ContextSelectionRule{
		ContextID: ctxOther.ID,
		Priority:  10,
		Conditions: []store.RuleCondition{
			{Field: "source_type", Operator: "eq", Value: "manifest"},
		},
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	t.Cleanup(func() {
		_ = s.DeleteRule(context.Background(), rule.ID)
	})

	resolved, isDefault, err := s.Resolve(ctx, map[string]any{"source_type": "url"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !isDefault {
		t.Errorf("isDefault = false; want true (no rule matched)")
	}
	if !resolved.IsDefault {
		t.Errorf("resolved.IsDefault = false; want true (fallback to default)")
	}
}

// TestContextResolution_PriorityOrder verifies that when two rules match the
// same metadata, the rule with the higher priority value wins.
func TestContextResolution_PriorityOrder(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	s := store.NewContextStore(db)

	// Low-priority context.
	ctxLow, err := s.Create(ctx, store.Context{
		Name:                  uniqueName("test-ctx-low-priority"),
		IsDefault:             false,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "low-model",
	})
	if err != nil {
		t.Fatalf("Create low-priority context: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Delete(context.Background(), ctxLow.ID)
	})

	// High-priority context.
	ctxHigh, err := s.Create(ctx, store.Context{
		Name:                  uniqueName("test-ctx-high-priority"),
		IsDefault:             false,
		SegmentationModel:     "kraken",
		TranscriptionProvider: "openai",
		TranscriptionModel:    "high-model",
	})
	if err != nil {
		t.Fatalf("Create high-priority context: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Delete(context.Background(), ctxHigh.ID)
	})

	// Both rules match the same metadata; the higher-priority rule should win.
	// Resolve orders by priority desc, so the rule with the larger priority
	// number is evaluated first.
	ruleLow, err := s.CreateRule(ctx, store.ContextSelectionRule{
		ContextID: ctxLow.ID,
		Priority:  1,
		Conditions: []store.RuleCondition{
			{Field: "source_type", Operator: "eq", Value: "manifest"},
		},
	})
	if err != nil {
		t.Fatalf("CreateRule (low priority): %v", err)
	}
	t.Cleanup(func() {
		_ = s.DeleteRule(context.Background(), ruleLow.ID)
	})

	ruleHigh, err := s.CreateRule(ctx, store.ContextSelectionRule{
		ContextID: ctxHigh.ID,
		Priority:  100,
		Conditions: []store.RuleCondition{
			{Field: "source_type", Operator: "eq", Value: "manifest"},
		},
	})
	if err != nil {
		t.Fatalf("CreateRule (high priority): %v", err)
	}
	t.Cleanup(func() {
		_ = s.DeleteRule(context.Background(), ruleHigh.ID)
	})

	resolved, isDefault, err := s.Resolve(ctx, map[string]any{"source_type": "manifest"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if isDefault {
		t.Errorf("isDefault = true; want false (a rule should have matched)")
	}
	if resolved.ID != ctxHigh.ID {
		t.Errorf("resolved.ID = %d; want %d (high-priority context)", resolved.ID, ctxHigh.ID)
	}
	if resolved.TranscriptionModel != "high-model" {
		t.Errorf("resolved.TranscriptionModel = %q; want %q", resolved.TranscriptionModel, "high-model")
	}
}
