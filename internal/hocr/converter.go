package hocr

import (
	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/ocradapter"
)

// ConvertHOCRLinesToXML serializes the derived box-only view with the shared writer.
func ConvertHOCRLinesToXML(lines []models.HOCRLine, width, height int) string {
	data, err := ocrpage.WriteHOCR(ocradapter.FromLines(lines, width, height), ocrpage.HOCROptions{})
	if err != nil {
		return ""
	}
	return string(data)
}
