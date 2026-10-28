package server

import (
	"testing"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func TestSegmentedPageRetainsGeometryThroughCanonicalValidation(t *testing.T) {
	page := ocrpage.Page{Width: 100, Height: 100, Regions: []ocrpage.Region{{
		ID: "heading", Type: ocrpage.RegionHeading, Box: ocrpage.Rect{X1: 5, Y1: 5, X2: 95, Y2: 40},
		Lines: []ocrpage.Line{{ID: "line", Box: ocrpage.Rect{X1: 10, Y1: 10, X2: 90, Y2: 30},
			Polygon:  []ocrpage.Point{{X: 10, Y: 10}, {X: 90, Y: 12}, {X: 90, Y: 30}, {X: 10, Y: 28}},
			Baseline: []ocrpage.Point{{X: 10, Y: 25}, {X: 90, Y: 27}}, Text: "Title"}},
	}}}
	items, err := pageAnnotations("item-image-1", "https://example.org/canvas/1", page)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := iiif.NewAnnotationPage(iiif.PageIdentity{PublicBaseURL: "https://example.org", ItemImageID: 1, CanvasURI: "https://example.org/canvas/1"}, items)
	if err != nil {
		t.Fatal(err)
	}
	if err := iiif.ValidateAnnotationPageGeometry(raw, 100, 100); err != nil {
		t.Fatal(err)
	}
	parsed, err := ocrpage.ReadIIIF(raw, ocrpage.IIIFReadOptions{CanvasWidth: 100, CanvasHeight: 100})
	if err != nil {
		t.Fatal(err)
	}
	lines := parsed.Page.Lines()
	if len(lines) != 1 || len(lines[0].Polygon) != 4 || len(lines[0].Baseline) != 2 || len(parsed.Page.Regions) != 1 || parsed.Page.Regions[0].Type != ocrpage.RegionHeading {
		t.Fatalf("canonical round trip lost geometry: %+v", parsed.Page)
	}
}
