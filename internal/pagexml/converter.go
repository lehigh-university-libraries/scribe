package pagexml

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
)

// PageXML structures for parsing

type PageXML struct {
	XMLName  xml.Name `xml:"PcGts"`
	Metadata Metadata `xml:"Metadata"`
	Page     Page     `xml:"Page"`
}

type Metadata struct {
	Creator  string `xml:"Creator"`
	Created  string `xml:"Created"`
	LastChange string `xml:"LastChange"`
}

type Page struct {
	ImageFilename string       `xml:"imageFilename,attr"`
	ImageWidth    int          `xml:"imageWidth,attr"`
	ImageHeight   int          `xml:"imageHeight,attr"`
	TextRegions   []TextRegion `xml:"TextRegion"`
}

type TextRegion struct {
	ID          string     `xml:"id,attr"`
	Type        string     `xml:"type,attr"`
	Coords      Coords     `xml:"Coords"`
	TextLines   []TextLine `xml:"TextLine"`
}

type TextLine struct {
	ID       string   `xml:"id,attr"`
	Coords   Coords   `xml:"Coords"`
	Baseline Baseline `xml:"Baseline"`
	TextEquiv *TextEquiv `xml:"TextEquiv,omitempty"`
}

type Baseline struct {
	Points string `xml:"points,attr"`
}

type Coords struct {
	Points string `xml:"points,attr"`
}

type TextEquiv struct {
	Unicode string `xml:"Unicode"`
}

// ParsePageXML parses PageXML from a reader
func ParsePageXML(r io.Reader) (*PageXML, error) {
	var pageXML PageXML
	decoder := xml.NewDecoder(r)
	if err := decoder.Decode(&pageXML); err != nil {
		return nil, fmt.Errorf("failed to parse PageXML: %w", err)
	}
	return &pageXML, nil
}

// parsePoints converts a PageXML points string to vertices
// PageXML format: "x1,y1 x2,y2 x3,y3 ..."
func parsePoints(pointsStr string) ([]models.Vertex, error) {
	pointsStr = strings.TrimSpace(pointsStr)
	if pointsStr == "" {
		return nil, fmt.Errorf("empty points string")
	}

	pairs := strings.Fields(pointsStr)
	vertices := make([]models.Vertex, 0, len(pairs))

	for _, pair := range pairs {
		coords := strings.Split(pair, ",")
		if len(coords) != 2 {
			return nil, fmt.Errorf("invalid point format: %s", pair)
		}

		x, err := strconv.Atoi(strings.TrimSpace(coords[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid x coordinate: %w", err)
		}

		y, err := strconv.Atoi(strings.TrimSpace(coords[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid y coordinate: %w", err)
		}

		vertices = append(vertices, models.Vertex{X: x, Y: y})
	}

	return vertices, nil
}

// getBoundingBox calculates the bounding box from vertices
func getBoundingBox(vertices []models.Vertex) models.BoundingPoly {
	if len(vertices) == 0 {
		return models.BoundingPoly{}
	}

	minX, minY := vertices[0].X, vertices[0].Y
	maxX, maxY := vertices[0].X, vertices[0].Y

	for _, v := range vertices[1:] {
		if v.X < minX {
			minX = v.X
		}
		if v.X > maxX {
			maxX = v.X
		}
		if v.Y < minY {
			minY = v.Y
		}
		if v.Y > maxY {
			maxY = v.Y
		}
	}

	// Return as a rectangle with 4 corners
	return models.BoundingPoly{
		Vertices: []models.Vertex{
			{X: minX, Y: minY},
			{X: maxX, Y: minY},
			{X: maxX, Y: maxY},
			{X: minX, Y: maxY},
		},
	}
}

// ConvertToOCRResponse converts PageXML to OCRResponse
func ConvertToOCRResponse(pageXML *PageXML) (models.OCRResponse, error) {
	var paragraphs []models.Paragraph

	// Process each text region
	for _, textRegion := range pageXML.Page.TextRegions {
		// Get region coordinates
		regionVertices, err := parsePoints(textRegion.Coords.Points)
		if err != nil {
			return models.OCRResponse{}, fmt.Errorf("failed to parse region coords: %w", err)
		}

		// Process each text line in the region
		var words []models.Word
		for _, textLine := range textRegion.TextLines {
			// Get text line coordinates
			lineVertices, err := parsePoints(textLine.Coords.Points)
			if err != nil {
				return models.OCRResponse{}, fmt.Errorf("failed to parse line coords: %w", err)
			}

			// Get the bounding box for this line
			lineBoundingBox := getBoundingBox(lineVertices)

			// Get text if available
			text := ""
			if textLine.TextEquiv != nil {
				text = textLine.TextEquiv.Unicode
			}

			// Create a word representing the entire line
			word := models.Word{
				BoundingBox: lineBoundingBox,
				Symbols: []models.Symbol{
					{
						BoundingBox: lineBoundingBox,
						Text:        text,
					},
				},
			}
			words = append(words, word)
		}

		// Create paragraph from this text region
		paragraph := models.Paragraph{
			BoundingBox: getBoundingBox(regionVertices),
			Words:       words,
		}
		paragraphs = append(paragraphs, paragraph)
	}

	// Create the block
	block := models.Block{
		BoundingBox: models.BoundingPoly{
			Vertices: []models.Vertex{
				{X: 0, Y: 0},
				{X: pageXML.Page.ImageWidth, Y: 0},
				{X: pageXML.Page.ImageWidth, Y: pageXML.Page.ImageHeight},
				{X: 0, Y: pageXML.Page.ImageHeight},
			},
		},
		BlockType:  "TEXT",
		Paragraphs: paragraphs,
	}

	// Create the page
	page := models.Page{
		Width:  pageXML.Page.ImageWidth,
		Height: pageXML.Page.ImageHeight,
		Blocks: []models.Block{block},
	}

	// Create the response
	return models.OCRResponse{
		Responses: []models.Response{
			{
				FullTextAnnotation: &models.FullTextAnnotation{
					Pages: []models.Page{page},
					Text:  "Laypa segmentation + LLM transcription",
				},
			},
		},
	}, nil
}
