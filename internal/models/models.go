package models

type HOCRLine struct {
	ID    string     `json:"id"`
	BBox  BBox       `json:"bbox"`
	Words []HOCRWord `json:"words"`
}

type HOCRWord struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	BBox       BBox    `json:"bbox"`
	Confidence float64 `json:"confidence"`
	LineID     string  `json:"line_id"`
}

type HOCRGlyph struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	BBox   BBox   `json:"bbox"`
	WordID string `json:"word_id"`
	LineID string `json:"line_id"`
	Index  int    `json:"index"`
}

type BBox struct {
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
	X2 int `json:"x2"`
	Y2 int `json:"y2"`
}

// Internal structures for OCR processing
type OCRResponse struct {
	Responses []Response `json:"responses"`
}

type Response struct {
	FullTextAnnotation *FullTextAnnotation `json:"fullTextAnnotation"`
}

type FullTextAnnotation struct {
	Pages []Page `json:"pages"`
	Text  string `json:"text"`
}

type Page struct {
	Property *Property `json:"property"`
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	Blocks   []Block   `json:"blocks"`
}

type Block struct {
	BoundingBox BoundingPoly `json:"boundingBox"`
	Paragraphs  []Paragraph  `json:"paragraphs"`
	BlockType   string       `json:"blockType"`
}

type Paragraph struct {
	BoundingBox BoundingPoly `json:"boundingBox"`
	Words       []Word       `json:"words"`
}

type Word struct {
	Property    *Property    `json:"property"`
	BoundingBox BoundingPoly `json:"boundingBox"`
	Symbols     []Symbol     `json:"symbols"`
}

type Symbol struct {
	Property    *Property    `json:"property"`
	BoundingBox BoundingPoly `json:"boundingBox"`
	Text        string       `json:"text"`
}

type Property struct {
	DetectedLanguages []DetectedLanguage `json:"detectedLanguages"`
}

type DetectedLanguage struct {
	LanguageCode string  `json:"languageCode"`
	Confidence   float64 `json:"confidence"`
}

type BoundingPoly struct {
	Vertices []Vertex `json:"vertices"`
}

type Vertex struct {
	X int `json:"x"`
	Y int `json:"y"`
}
