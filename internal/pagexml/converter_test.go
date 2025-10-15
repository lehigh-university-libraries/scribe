package pagexml

import (
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
)

func TestParsePageXML(t *testing.T) {
	tests := []struct {
		name    string
		xml     string
		wantErr bool
		check   func(*testing.T, *PageXML)
	}{
		{
			name: "valid PageXML with single text region",
			xml: `<?xml version="1.0" encoding="UTF-8"?>
<PcGts xmlns="http://schema.primaresearch.org/PAGE/gts/pagecontent/2013-07-15">
	<Metadata>
		<Creator>Laypa</Creator>
		<Created>2024-01-01</Created>
		<LastChange>2024-01-01</LastChange>
	</Metadata>
	<Page imageFilename="test.jpg" imageWidth="1000" imageHeight="800">
		<TextRegion id="region_1" type="paragraph">
			<Coords points="100,100 900,100 900,200 100,200"/>
			<TextLine id="line_1">
				<Coords points="100,100 900,100 900,150 100,150"/>
				<Baseline points="100,140 900,140"/>
				<TextEquiv>
					<Unicode>Hello World</Unicode>
				</TextEquiv>
			</TextLine>
		</TextRegion>
	</Page>
</PcGts>`,
			wantErr: false,
			check: func(t *testing.T, p *PageXML) {
				if p.Page.ImageWidth != 1000 {
					t.Errorf("ImageWidth = %d, want 1000", p.Page.ImageWidth)
				}
				if p.Page.ImageHeight != 800 {
					t.Errorf("ImageHeight = %d, want 800", p.Page.ImageHeight)
				}
				if len(p.Page.TextRegions) != 1 {
					t.Fatalf("len(TextRegions) = %d, want 1", len(p.Page.TextRegions))
				}
				region := p.Page.TextRegions[0]
				if region.ID != "region_1" {
					t.Errorf("Region ID = %s, want region_1", region.ID)
				}
				if len(region.TextLines) != 1 {
					t.Fatalf("len(TextLines) = %d, want 1", len(region.TextLines))
				}
				line := region.TextLines[0]
				if line.TextEquiv == nil || line.TextEquiv.Unicode != "Hello World" {
					t.Errorf("TextLine text = %v, want 'Hello World'", line.TextEquiv)
				}
			},
		},
		{
			name: "PageXML with multiple text lines",
			xml: `<?xml version="1.0" encoding="UTF-8"?>
<PcGts>
	<Metadata>
		<Creator>Laypa</Creator>
	</Metadata>
	<Page imageFilename="test.jpg" imageWidth="1000" imageHeight="800">
		<TextRegion id="region_1" type="paragraph">
			<Coords points="100,100 900,100 900,300 100,300"/>
			<TextLine id="line_1">
				<Coords points="100,100 900,100 900,150 100,150"/>
				<Baseline points="100,140 900,140"/>
				<TextEquiv><Unicode>First line</Unicode></TextEquiv>
			</TextLine>
			<TextLine id="line_2">
				<Coords points="100,200 900,200 900,250 100,250"/>
				<Baseline points="100,240 900,240"/>
				<TextEquiv><Unicode>Second line</Unicode></TextEquiv>
			</TextLine>
		</TextRegion>
	</Page>
</PcGts>`,
			wantErr: false,
			check: func(t *testing.T, p *PageXML) {
				if len(p.Page.TextRegions) != 1 {
					t.Fatalf("len(TextRegions) = %d, want 1", len(p.Page.TextRegions))
				}
				region := p.Page.TextRegions[0]
				if len(region.TextLines) != 2 {
					t.Fatalf("len(TextLines) = %d, want 2", len(region.TextLines))
				}
				if region.TextLines[0].TextEquiv.Unicode != "First line" {
					t.Errorf("First line text = %s, want 'First line'", region.TextLines[0].TextEquiv.Unicode)
				}
				if region.TextLines[1].TextEquiv.Unicode != "Second line" {
					t.Errorf("Second line text = %s, want 'Second line'", region.TextLines[1].TextEquiv.Unicode)
				}
			},
		},
		{
			name:    "invalid XML",
			xml:     `<invalid>xml`,
			wantErr: true,
		},
		{
			name:    "empty XML",
			xml:     ``,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := strings.NewReader(tt.xml)
			pageXML, err := ParsePageXML(reader)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePageXML() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.check != nil {
				tt.check(t, pageXML)
			}
		})
	}
}

func TestParsePoints(t *testing.T) {
	tests := []struct {
		name      string
		pointsStr string
		wantLen   int
		wantFirst models.Vertex
		wantLast  models.Vertex
		wantErr   bool
	}{
		{
			name:      "valid points",
			pointsStr: "100,200 300,400 500,600",
			wantLen:   3,
			wantFirst: models.Vertex{X: 100, Y: 200},
			wantLast:  models.Vertex{X: 500, Y: 600},
			wantErr:   false,
		},
		{
			name:      "single point",
			pointsStr: "10,20",
			wantLen:   1,
			wantFirst: models.Vertex{X: 10, Y: 20},
			wantErr:   false,
		},
		{
			name:      "points with extra whitespace",
			pointsStr: "  100,200   300,400  ",
			wantLen:   2,
			wantFirst: models.Vertex{X: 100, Y: 200},
			wantLast:  models.Vertex{X: 300, Y: 400},
			wantErr:   false,
		},
		{
			name:      "empty string",
			pointsStr: "",
			wantErr:   true,
		},
		{
			name:      "invalid format - missing comma",
			pointsStr: "100 200",
			wantErr:   true,
		},
		{
			name:      "invalid format - non-numeric",
			pointsStr: "abc,def",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vertices, err := parsePoints(tt.pointsStr)

			if (err != nil) != tt.wantErr {
				t.Errorf("parsePoints() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			if len(vertices) != tt.wantLen {
				t.Errorf("len(vertices) = %d, want %d", len(vertices), tt.wantLen)
				return
			}

			if vertices[0] != tt.wantFirst {
				t.Errorf("first vertex = %+v, want %+v", vertices[0], tt.wantFirst)
			}

			if tt.wantLen > 1 && vertices[len(vertices)-1] != tt.wantLast {
				t.Errorf("last vertex = %+v, want %+v", vertices[len(vertices)-1], tt.wantLast)
			}
		})
	}
}

func TestGetBoundingBox(t *testing.T) {
	tests := []struct {
		name     string
		vertices []models.Vertex
		want     models.BoundingPoly
	}{
		{
			name: "rectangle",
			vertices: []models.Vertex{
				{X: 100, Y: 100},
				{X: 200, Y: 100},
				{X: 200, Y: 200},
				{X: 100, Y: 200},
			},
			want: models.BoundingPoly{
				Vertices: []models.Vertex{
					{X: 100, Y: 100},
					{X: 200, Y: 100},
					{X: 200, Y: 200},
					{X: 100, Y: 200},
				},
			},
		},
		{
			name: "irregular polygon",
			vertices: []models.Vertex{
				{X: 50, Y: 150},
				{X: 250, Y: 100},
				{X: 300, Y: 250},
				{X: 75, Y: 200},
			},
			want: models.BoundingPoly{
				Vertices: []models.Vertex{
					{X: 50, Y: 100},
					{X: 300, Y: 100},
					{X: 300, Y: 250},
					{X: 50, Y: 250},
				},
			},
		},
		{
			name:     "empty vertices",
			vertices: []models.Vertex{},
			want:     models.BoundingPoly{},
		},
		{
			name: "single point",
			vertices: []models.Vertex{
				{X: 100, Y: 200},
			},
			want: models.BoundingPoly{
				Vertices: []models.Vertex{
					{X: 100, Y: 200},
					{X: 100, Y: 200},
					{X: 100, Y: 200},
					{X: 100, Y: 200},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getBoundingBox(tt.vertices)

			if len(got.Vertices) != len(tt.want.Vertices) {
				t.Errorf("getBoundingBox() vertices length = %d, want %d", len(got.Vertices), len(tt.want.Vertices))
				return
			}

			for i, v := range got.Vertices {
				if v != tt.want.Vertices[i] {
					t.Errorf("getBoundingBox() vertex[%d] = %+v, want %+v", i, v, tt.want.Vertices[i])
				}
			}
		})
	}
}

func TestConvertToOCRResponse(t *testing.T) {
	tests := []struct {
		name    string
		pageXML *PageXML
		check   func(*testing.T, models.OCRResponse)
		wantErr bool
	}{
		{
			name: "single text region with one line",
			pageXML: &PageXML{
				Page: Page{
					ImageWidth:  1000,
					ImageHeight: 800,
					TextRegions: []TextRegion{
						{
							ID:   "region_1",
							Type: "paragraph",
							Coords: Coords{
								Points: "100,100 900,100 900,200 100,200",
							},
							TextLines: []TextLine{
								{
									ID: "line_1",
									Coords: Coords{
										Points: "100,100 900,100 900,150 100,150",
									},
									Baseline: Baseline{
										Points: "100,140 900,140",
									},
									TextEquiv: &TextEquiv{
										Unicode: "Test text",
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
			check: func(t *testing.T, resp models.OCRResponse) {
				if len(resp.Responses) != 1 {
					t.Fatalf("len(Responses) = %d, want 1", len(resp.Responses))
				}
				if resp.Responses[0].FullTextAnnotation == nil {
					t.Fatal("FullTextAnnotation is nil")
				}
				pages := resp.Responses[0].FullTextAnnotation.Pages
				if len(pages) != 1 {
					t.Fatalf("len(Pages) = %d, want 1", len(pages))
				}
				if pages[0].Width != 1000 {
					t.Errorf("Page width = %d, want 1000", pages[0].Width)
				}
				if pages[0].Height != 800 {
					t.Errorf("Page height = %d, want 800", pages[0].Height)
				}
				if len(pages[0].Blocks) != 1 {
					t.Fatalf("len(Blocks) = %d, want 1", len(pages[0].Blocks))
				}
				if len(pages[0].Blocks[0].Paragraphs) != 1 {
					t.Fatalf("len(Paragraphs) = %d, want 1", len(pages[0].Blocks[0].Paragraphs))
				}
				para := pages[0].Blocks[0].Paragraphs[0]
				if len(para.Words) != 1 {
					t.Fatalf("len(Words) = %d, want 1", len(para.Words))
				}
				word := para.Words[0]
				if len(word.Symbols) != 1 {
					t.Fatalf("len(Symbols) = %d, want 1", len(word.Symbols))
				}
				if word.Symbols[0].Text != "Test text" {
					t.Errorf("Symbol text = %s, want 'Test text'", word.Symbols[0].Text)
				}
			},
		},
		{
			name: "multiple text regions",
			pageXML: &PageXML{
				Page: Page{
					ImageWidth:  1000,
					ImageHeight: 800,
					TextRegions: []TextRegion{
						{
							ID: "region_1",
							Coords: Coords{
								Points: "100,100 500,100 500,200 100,200",
							},
							TextLines: []TextLine{
								{
									ID: "line_1",
									Coords: Coords{
										Points: "100,100 500,100 500,150 100,150",
									},
									TextEquiv: &TextEquiv{Unicode: "First region"},
								},
							},
						},
						{
							ID: "region_2",
							Coords: Coords{
								Points: "100,300 500,300 500,400 100,400",
							},
							TextLines: []TextLine{
								{
									ID: "line_2",
									Coords: Coords{
										Points: "100,300 500,300 500,350 100,350",
									},
									TextEquiv: &TextEquiv{Unicode: "Second region"},
								},
							},
						},
					},
				},
			},
			wantErr: false,
			check: func(t *testing.T, resp models.OCRResponse) {
				pages := resp.Responses[0].FullTextAnnotation.Pages
				if len(pages[0].Blocks[0].Paragraphs) != 2 {
					t.Fatalf("len(Paragraphs) = %d, want 2", len(pages[0].Blocks[0].Paragraphs))
				}
				para1 := pages[0].Blocks[0].Paragraphs[0]
				para2 := pages[0].Blocks[0].Paragraphs[1]
				if para1.Words[0].Symbols[0].Text != "First region" {
					t.Errorf("First paragraph text = %s, want 'First region'", para1.Words[0].Symbols[0].Text)
				}
				if para2.Words[0].Symbols[0].Text != "Second region" {
					t.Errorf("Second paragraph text = %s, want 'Second region'", para2.Words[0].Symbols[0].Text)
				}
			},
		},
		{
			name: "text line without text equiv",
			pageXML: &PageXML{
				Page: Page{
					ImageWidth:  1000,
					ImageHeight: 800,
					TextRegions: []TextRegion{
						{
							ID: "region_1",
							Coords: Coords{
								Points: "100,100 500,100 500,200 100,200",
							},
							TextLines: []TextLine{
								{
									ID: "line_1",
									Coords: Coords{
										Points: "100,100 500,100 500,150 100,150",
									},
									TextEquiv: nil, // No text
								},
							},
						},
					},
				},
			},
			wantErr: false,
			check: func(t *testing.T, resp models.OCRResponse) {
				pages := resp.Responses[0].FullTextAnnotation.Pages
				para := pages[0].Blocks[0].Paragraphs[0]
				if para.Words[0].Symbols[0].Text != "" {
					t.Errorf("Symbol text = %s, want empty string", para.Words[0].Symbols[0].Text)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := ConvertToOCRResponse(tt.pageXML)

			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertToOCRResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}

func TestConvertToOCRResponseBoundingBoxes(t *testing.T) {
	pageXML := &PageXML{
		Page: Page{
			ImageWidth:  1000,
			ImageHeight: 800,
			TextRegions: []TextRegion{
				{
					ID: "region_1",
					Coords: Coords{
						Points: "100,100 900,100 900,200 100,200",
					},
					TextLines: []TextLine{
						{
							ID: "line_1",
							Coords: Coords{
								Points: "150,120 850,120 850,180 150,180",
							},
							TextEquiv: &TextEquiv{Unicode: "Test"},
						},
					},
				},
			},
		},
	}

	resp, err := ConvertToOCRResponse(pageXML)
	if err != nil {
		t.Fatalf("ConvertToOCRResponse() error = %v", err)
	}

	// Check page-level bounding box
	pages := resp.Responses[0].FullTextAnnotation.Pages
	blockBBox := pages[0].Blocks[0].BoundingBox
	if len(blockBBox.Vertices) != 4 {
		t.Errorf("Block bounding box has %d vertices, want 4", len(blockBBox.Vertices))
	}
	// Block should span full image
	if blockBBox.Vertices[0].X != 0 || blockBBox.Vertices[0].Y != 0 {
		t.Errorf("Block top-left = (%d,%d), want (0,0)", blockBBox.Vertices[0].X, blockBBox.Vertices[0].Y)
	}
	if blockBBox.Vertices[2].X != 1000 || blockBBox.Vertices[2].Y != 800 {
		t.Errorf("Block bottom-right = (%d,%d), want (1000,800)", blockBBox.Vertices[2].X, blockBBox.Vertices[2].Y)
	}

	// Check paragraph bounding box (from region coords)
	paraBBox := pages[0].Blocks[0].Paragraphs[0].BoundingBox
	if paraBBox.Vertices[0].X != 100 || paraBBox.Vertices[0].Y != 100 {
		t.Errorf("Paragraph top-left = (%d,%d), want (100,100)", paraBBox.Vertices[0].X, paraBBox.Vertices[0].Y)
	}
	if paraBBox.Vertices[2].X != 900 || paraBBox.Vertices[2].Y != 200 {
		t.Errorf("Paragraph bottom-right = (%d,%d), want (900,200)", paraBBox.Vertices[2].X, paraBBox.Vertices[2].Y)
	}

	// Check word bounding box (from line coords)
	wordBBox := pages[0].Blocks[0].Paragraphs[0].Words[0].BoundingBox
	if wordBBox.Vertices[0].X != 150 || wordBBox.Vertices[0].Y != 120 {
		t.Errorf("Word top-left = (%d,%d), want (150,120)", wordBBox.Vertices[0].X, wordBBox.Vertices[0].Y)
	}
	if wordBBox.Vertices[2].X != 850 || wordBBox.Vertices[2].Y != 180 {
		t.Errorf("Word bottom-right = (%d,%d), want (850,180)", wordBBox.Vertices[2].X, wordBBox.Vertices[2].Y)
	}
}
