package metrics

import (
	"regexp"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/models"
)

func CalculateAccuracyMetrics(original, transcribed string) models.EvalResult {
	origNorm := normalizeText(original)
	transNorm := normalizeText(transcribed)
	charSim := calculateSimilarity(origNorm, transNorm)
	origWords := strings.Fields(origNorm)
	transWords := strings.Fields(transNorm)
	wordSim := calculateSimilarity(strings.Join(origWords, " "), strings.Join(transWords, " "))
	wordAcc, correct, subs, dels, ins := calculateWordLevelMetrics(origWords, transWords)

	wer := 1.0 - wordAcc

	return models.EvalResult{
		CharacterSimilarity:   charSim,
		WordSimilarity:        wordSim,
		WordAccuracy:          wordAcc,
		WordErrorRate:         wer,
		TotalWordsOriginal:    len(origWords),
		TotalWordsTranscribed: len(transWords),
		CorrectWords:          correct,
		Substitutions:         subs,
		Deletions:             dels,
		Insertions:            ins,
	}
}

func normalizeText(text string) string {
	re := regexp.MustCompile(`\s+`)
	text = re.ReplaceAllString(strings.TrimSpace(text), " ")
	return strings.ToLower(text)
}

func LevenshteinDistance(a, b string) int {
	return levenshteinDistance(normalizeText(a), normalizeText(b))
}

func levenshteinDistance(s1, s2 string) int {
	len1, len2 := len(s1), len(s2)
	if len1 == 0 {
		return len2
	}
	if len2 == 0 {
		return len1
	}

	matrix := make([][]int, len1+1)
	for i := range matrix {
		matrix[i] = make([]int, len2+1)
	}

	for i := 0; i <= len1; i++ {
		matrix[i][0] = i
	}
	for j := 0; j <= len2; j++ {
		matrix[0][j] = j
	}

	for i := 1; i <= len1; i++ {
		for j := 1; j <= len2; j++ {
			cost := 0
			if s1[i-1] != s2[j-1] {
				cost = 1
			}
			matrix[i][j] = min(
				min(matrix[i-1][j]+1, matrix[i][j-1]+1),
				matrix[i-1][j-1]+cost,
			)
		}
	}

	return matrix[len1][len2]
}

func calculateSimilarity(s1, s2 string) float64 {
	maxLen := max(len(s1), len(s2))
	if maxLen == 0 {
		return 1.0
	}
	distance := levenshteinDistance(s1, s2)
	return 1.0 - float64(distance)/float64(maxLen)
}

func calculateWordLevelMetrics(orig, trans []string) (float64, int, int, int, int) {
	m, n := len(orig), len(trans)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := 0; i <= m; i++ {
		dp[i][0] = i
	}
	for j := 0; j <= n; j++ {
		dp[0][j] = j
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if orig[i-1] == trans[j-1] {
				dp[i][j] = dp[i-1][j-1]
			} else {
				dp[i][j] = 1 + min(
					min(dp[i-1][j], dp[i][j-1]),
					dp[i-1][j-1],
				)
			}
		}
	}

	i, j := m, n
	substitutions, deletions, insertions, correct := 0, 0, 0, 0

	for i > 0 || j > 0 {
		if i > 0 && j > 0 && orig[i-1] == trans[j-1] {
			correct++
			i--
			j--
		} else if i > 0 && j > 0 && dp[i][j] == dp[i-1][j-1]+1 {
			substitutions++
			i--
			j--
		} else if i > 0 && dp[i][j] == dp[i-1][j]+1 {
			deletions++
			i--
		} else if j > 0 && dp[i][j] == dp[i][j-1]+1 {
			insertions++
			j--
		}
	}

	totalEdits := substitutions + deletions + insertions
	wer := 0.0
	if m > 0 {
		wer = float64(totalEdits) / float64(m)
	}
	wordAccuracy := 1.0 - wer

	return wordAccuracy, correct, substitutions, deletions, insertions
}
