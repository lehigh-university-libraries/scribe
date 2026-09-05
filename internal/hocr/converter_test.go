package hocr

import (
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/models"
)

func TestConvertHOCRLinesToXMLEscapesCanonicalIdentifiersAndText(t *testing.T) {
	output := ConvertHOCRLinesToXML([]models.HOCRLine{{
		ID: "line' onclick='alert(1)",
		Words: []models.HOCRWord{{
			ID:   "word' onmouseover='alert(2)",
			Text: "<script>alert(3)</script>",
		}},
	}}, 100, 100)

	for _, unsafe := range []string{"onclick='alert(1)'", "onmouseover='alert(2)'", "<script>alert(3)</script>"} {
		if strings.Contains(output, unsafe) {
			t.Fatalf("hOCR output contains unescaped value %q:\n%s", unsafe, output)
		}
	}
	for _, escaped := range []string{"line&#39; onclick=&#39;alert(1)", "word&#39; onmouseover=&#39;alert(2)", "&lt;script&gt;alert(3)&lt;/script&gt;"} {
		if !strings.Contains(output, escaped) {
			t.Fatalf("hOCR output does not contain escaped value %q:\n%s", escaped, output)
		}
	}
}
