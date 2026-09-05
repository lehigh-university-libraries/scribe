package hocr

import (
	"fmt"
	"html"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/models"
)

// ConvertHOCRLinesToXML serializes canonical OCR lines as an hOCR document.
func ConvertHOCRLinesToXML(lines []models.HOCRLine, pageWidth, pageHeight int) string {
	var hocr strings.Builder

	hocr.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	hocr.WriteString("<!DOCTYPE html PUBLIC \"-//W3C//DTD XHTML 1.0 Transitional//EN\"\n")
	hocr.WriteString("    \"http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd\">\n")
	hocr.WriteString("<html xmlns=\"http://www.w3.org/1999/xhtml\" xml:lang=\"en\" lang=\"en\">\n")
	hocr.WriteString("<head>\n")
	hocr.WriteString("<title></title>\n")
	hocr.WriteString("<meta http-equiv=\"Content-Type\" content=\"text/html; charset=utf-8\" />\n")
	hocr.WriteString("<meta name='ocr-system' content='custom-word-detection-with-chatgpt' />\n")
	hocr.WriteString("<meta name='ocr-capabilities' content='ocr_page ocr_carea ocr_par ocr_line ocrx_word' />\n")
	hocr.WriteString("</head>\n")
	hocr.WriteString("<body>\n")

	bbox := fmt.Sprintf("bbox 0 0 %d %d", pageWidth, pageHeight)
	fmt.Fprintf(&hocr, "<div class='ocr_page' id='page_1' title='%s'>\n", bbox)

	for _, line := range lines {
		hocr.WriteString(convertHOCRLineToXML(line))
	}

	hocr.WriteString("</div>\n")
	hocr.WriteString("</body>\n")
	hocr.WriteString("</html>\n")

	return hocr.String()
}

func convertHOCRLineToXML(line models.HOCRLine) string {
	bbox := fmt.Sprintf("bbox %d %d %d %d", line.BBox.X1, line.BBox.Y1, line.BBox.X2, line.BBox.Y2)

	var lineBuilder strings.Builder
	fmt.Fprintf(&lineBuilder, "<span class='ocr_line' id='%s' title='%s'>", html.EscapeString(line.ID), bbox)

	for _, word := range line.Words {
		wordXML := convertHOCRWordToXML(word)
		lineBuilder.WriteString(wordXML)
	}

	lineBuilder.WriteString("</span>\n")
	return lineBuilder.String()
}

func convertHOCRWordToXML(word models.HOCRWord) string {
	bbox := fmt.Sprintf("bbox %d %d %d %d", word.BBox.X1, word.BBox.Y1, word.BBox.X2, word.BBox.Y2)
	confidence := fmt.Sprintf("; x_wconf %.0f", word.Confidence)
	title := bbox + confidence

	return fmt.Sprintf("<span class='ocrx_word' id='%s' title='%s'>%s</span> ",
		html.EscapeString(word.ID), title, html.EscapeString(word.Text))
}
