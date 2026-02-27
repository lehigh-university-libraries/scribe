package server

import (
	"html"
	"regexp"
	"strings"

	legacyhandlers "github.com/lehigh-university-libraries/hOCRedit/internal/handlers"
)

var (
	reLineBlock = regexp.MustCompile(`(?is)<[^>]*class=["'][^"']*\bocr_line\b[^"']*["'][^>]*>(.*?)</[^>]+>`)
	reWordBlock = regexp.MustCompile(`(?is)<[^>]*class=["'][^"']*\bocrx_word\b[^"']*["'][^>]*>(.*?)</[^>]+>`)
	reAnyTag    = regexp.MustCompile(`(?is)<[^>]+>`)
	reSpace     = regexp.MustCompile(`\s+`)
)

func hocrToPlainTextLenient(hocrXML string) string {
	if out, err := legacyhandlers.HOCRToPlainText(hocrXML); err == nil {
		return out
	}

	lines := reLineBlock.FindAllStringSubmatch(hocrXML, -1)
	if len(lines) == 0 {
		return normalizeText(hocrXML)
	}

	out := make([]string, 0, len(lines))
	for _, m := range lines {
		block := m[1]
		words := reWordBlock.FindAllStringSubmatch(block, -1)
		if len(words) > 0 {
			parts := make([]string, 0, len(words))
			for _, w := range words {
				t := normalizeText(w[1])
				if t != "" {
					parts = append(parts, t)
				}
			}
			if len(parts) > 0 {
				out = append(out, strings.Join(parts, " "))
				continue
			}
		}

		t := normalizeText(block)
		if t != "" {
			out = append(out, t)
		}
	}

	return strings.Join(out, "\n")
}

func normalizeText(s string) string {
	noTags := reAnyTag.ReplaceAllString(s, " ")
	decoded := html.UnescapeString(noTags)
	return strings.TrimSpace(reSpace.ReplaceAllString(decoded, " "))
}

