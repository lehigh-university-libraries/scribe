package worddetection

import (
	"reflect"
	"testing"
)

func TestNewspaperReadingOrderPreservesColumnsAndMarginalia(t *testing.T) {
	regions, err := parseLayoutRegions([]byte(`{"boxes":[{"label":"text","coordinate":[110,20,210,200],"order":2},{"label":"text","coordinate":[10,20,100,200],"order":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	left1 := WordBox{X: 10, Y: 20, Width: 90, Height: 10}
	right1 := WordBox{X: 110, Y: 20, Width: 90, Height: 10}
	left2 := WordBox{X: 10, Y: 40, Width: 90, Height: 10}
	right2 := WordBox{X: 110, Y: 40, Width: 90, Height: 10}
	margin := WordBox{X: 220, Y: 50, Width: 15, Height: 10}
	got := orderNewspaperLines([]WordBox{right1, left1, right2, left2, margin}, regions)
	want := []WordBox{left1, left2, right1, right2, margin}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("line geometry/order = %+v; want %+v", got, want)
	}
	for _, invalid := range []string{`{}`, `{"boxes":[{"label":"text","coordinate":[0,0,10,10]}]}`, `{"boxes":[{"label":"text","coordinate":[10,0,0,10],"order":0}]}`} {
		if _, err := parseLayoutRegions([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid layout: %s", invalid)
		}
	}
}

func TestNewspaperSparseLayoutRetainsNativeLines(t *testing.T) {
	for _, data := range []string{`{"boxes":[]}`, `{"boxes":[{"label":"header","coordinate":[0,0,90,10],"order":null}]}`} {
		regions, err := parseLayoutRegions([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		lines := []WordBox{{X: 0, Y: 0, Width: 90, Height: 10}}
		if got := orderNewspaperLines(lines, regions); !reflect.DeepEqual(got, lines) {
			t.Fatalf("sparse layout dropped lines: %+v", got)
		}
	}
}

func TestNewspaperOrderingKeepsEveryDetectedLineOnceInOverlappingRegions(t *testing.T) {
	regions, err := parseLayoutRegions([]byte(`{"boxes":[{"label":"text","coordinate":[0,0,90,90],"order":1},{"label":"text","coordinate":[50,0,150,90],"order":2},{"label":"image","coordinate":[0,0,150,90]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	line := WordBox{X: 80, Y: 10, Width: 60, Height: 10}
	got := orderNewspaperLines([]WordBox{line}, regions)
	if !reflect.DeepEqual(got, []WordBox{line}) {
		t.Fatalf("line lost, cropped, or duplicated: %+v", got)
	}
}

func TestNewspaperAcceptsUnrankedAuxiliaryRegionsWithoutDroppingLines(t *testing.T) {
	regions, err := parseLayoutRegions([]byte(`{"boxes":[{"label":"text","coordinate":[0,20,90,90],"order":1},{"label":"header","coordinate":[0,0,90,10],"order":null},{"label":"footnote","coordinate":[0,100,90,110],"order":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	header := WordBox{X: 0, Y: 0, Width: 90, Height: 10}
	body := WordBox{X: 0, Y: 20, Width: 90, Height: 10}
	footnote := WordBox{X: 0, Y: 100, Width: 90, Height: 10}
	got := orderNewspaperLines([]WordBox{header, body, footnote}, regions)
	if want := []WordBox{body, header, footnote}; !reflect.DeepEqual(got, want) {
		t.Fatalf("line geometry/order = %+v; want %+v", got, want)
	}
}
