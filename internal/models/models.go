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
