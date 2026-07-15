package metrics

import "testing"

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b     string
		expected int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "a", 1},
		{"kitten", "kitten", 0},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"gumbo", "gambol", 2},
		{"book", "back", 2},
		{"a", "b", 1},
		{"abc", "yabd", 2},
		{"intention", "execution", 5},
		{"distance", "difference", 5},
		{"abcdef", "azced", 3},
		{"Saturday", "Sunday", 3},
		{"abcdef", "abcdef", 0},
		{"abcdef", "abcdeg", 1},
		{"abc", "abcdef", 3},
		{"abcdef", "abc", 3},
		{"longstringwithmanychars", "longstringwithanychars", 1},
		{"1234567890", "0987654321", 10},
		{"école", "ecole", 1},
		{"你好世界", "你好世", 1},
		{"مرحبا", "مرحبه", 1},
	}

	for _, tt := range tests {
		got := levenshteinDistance(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("LevenshteinDistance(%q, %q) = %d; want %d",
				tt.a, tt.b, got, tt.expected)
		}
	}
}

func TestCalculateSimilarityUsesRunes(t *testing.T) {
	got := calculateSimilarity("école", "ecole")
	want := 0.8
	if got != want {
		t.Fatalf("calculateSimilarity() = %v; want %v", got, want)
	}
}
