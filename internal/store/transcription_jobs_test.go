package store

import "testing"

func TestItemImageIDFromSubject(t *testing.T) {
	got, ok := itemImageIDFromSubject("item-images/123")
	if !ok || got != 123 {
		t.Fatalf("itemImageIDFromSubject valid subject = %d/%v, want 123/true", got, ok)
	}

	for _, subject := range []string{
		"",
		"items/123",
		"item-images/",
		"item-images/123/extra",
		"item-images/not-a-number",
		"item-images/0",
	} {
		t.Run(subject, func(t *testing.T) {
			if got, ok := itemImageIDFromSubject(subject); ok || got != 0 {
				t.Fatalf("itemImageIDFromSubject(%q) = %d/%v, want 0/false", subject, got, ok)
			}
		})
	}
}
