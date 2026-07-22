package iiif_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func TestMediaFragmentPixelXYWHPreservesOtherDimensions(t *testing.T) {
	t.Parallel()
	fragment := "t=10,20&xywh=pixel:1,2,30,40&track=video"
	xywh, present, err := iiif.MediaFragmentPixelXYWH(fragment)
	if err != nil {
		t.Fatal(err)
	}
	if !present || xywh != "pixel:1,2,30,40" {
		t.Fatalf("xywh = %q, %v", xywh, present)
	}
	updated, err := iiif.ReplaceMediaFragmentPixelXYWH(fragment, "5,6,70,80")
	if err != nil {
		t.Fatal(err)
	}
	if updated != "t=10,20&xywh=pixel:5,6,70,80&track=video" {
		t.Fatalf("updated fragment = %q", updated)
	}
	withoutGeometry, err := iiif.RemoveMediaFragmentPixelXYWH(updated)
	if err != nil {
		t.Fatal(err)
	}
	if withoutGeometry != "t=10,20&track=video" {
		t.Fatalf("non-spatial fragment = %q", withoutGeometry)
	}
}

func TestReplaceCompactTargetPreservesFragmentDimensions(t *testing.T) {
	t.Parallel()
	target, err := iiif.ReplaceTargetPixelXYWH(
		"https://images.example/canvas/1?view=primary#t=10,20&xywh=pixel:1,2,30,40&track=video",
		"https://images.example/canvas/1?view=primary",
		"5,6,70,80",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := iiif.TargetCanvas(target); got != "https://images.example/canvas/1?view=primary" {
		t.Fatalf("canvas = %q", got)
	}
	xywh, present, err := iiif.TargetPixelXYWH(target)
	if err != nil || !present || xywh != "pixel:5,6,70,80" {
		t.Fatalf("target xywh = %q, %v, %v", xywh, present, err)
	}
	selector := target["selector"].(map[string]any)
	if got := selector["value"]; got != "t=10,20&xywh=pixel:5,6,70,80&track=video" {
		t.Fatalf("selector value = %q", got)
	}
}

func TestTargetPixelXYWHRejectsAmbiguousGeometry(t *testing.T) {
	t.Parallel()
	target := map[string]any{
		"source": "https://images.example/canvas/1",
		"selector": []any{
			map[string]any{"type": "FragmentSelector", "value": "xywh=1,2,3,4"},
			map[string]any{"type": "FragmentSelector", "value": "t=1,2&xywh=5,6,7,8"},
		},
	}
	if _, _, err := iiif.TargetPixelXYWH(target); err == nil {
		t.Fatal("accepted multiple spatial FragmentSelectors")
	}
}

func TestCanvasReferenceTargetAndSingleArrayAreUnambiguous(t *testing.T) {
	t.Parallel()
	canvas := map[string]any{
		"id": "https://images.example/canvas/1", "type": "Canvas",
		"label": map[string]any{"en": []any{"Page one"}},
	}
	if got := iiif.TargetCanvas(canvas); got != "https://images.example/canvas/1" {
		t.Fatalf("Canvas reference target = %q", got)
	}
	if got := iiif.TargetCanvas([]any{canvas}); got != "https://images.example/canvas/1" {
		t.Fatalf("single target array Canvas = %q", got)
	}
	if got := iiif.TargetCanvas([]any{canvas, canvas}); got != "" {
		t.Fatalf("multi-target Canvas = %q, want ambiguous", got)
	}
	if _, _, err := iiif.TargetPixelXYWH([]any{canvas, canvas}); err == nil {
		t.Fatal("multiple canonical targets were not rejected")
	}
}

func TestReplaceCanvasReferenceTargetPreservesReferenceProperties(t *testing.T) {
	t.Parallel()
	original := map[string]any{
		"id": "https://images.example/canvas/1", "type": "Canvas",
		"label":     map[string]any{"en": []any{"Page one"}},
		"thumbnail": []any{map[string]any{"id": "https://images.example/thumb.jpg", "type": "Image"}},
	}
	target, err := iiif.ReplaceTargetPixelXYWH(original, "https://images.example/canvas/2", "1,2,30,40")
	if err != nil {
		t.Fatal(err)
	}
	source, ok := target["source"].(map[string]any)
	if !ok {
		t.Fatalf("source = %#v", target["source"])
	}
	if source["id"] != "https://images.example/canvas/2" || source["type"] != "Canvas" {
		t.Fatalf("source identity = %#v", source)
	}
	if !reflect.DeepEqual(source["label"], original["label"]) || !reflect.DeepEqual(source["thumbnail"], original["thumbnail"]) {
		t.Fatalf("Canvas reference properties were not preserved: %#v", source)
	}
	if xywh, present, err := iiif.TargetPixelXYWH(target); err != nil || !present || xywh != "1,2,30,40" {
		t.Fatalf("target xywh = %q, %t, %v", xywh, present, err)
	}
}

func TestNormalizeAnnotationPagePreservesUnknownLargeIntegers(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "@context": "http://iiif.io/api/presentation/3/context.json",
  "type": "AnnotationPage",
  "items": [{
    "type": "Annotation",
    "motivation": "supplementing",
    "textGranularity": "line",
    "body": {"type": "TextualBody", "value": "Hello", "service": [{
      "id": "https://scribe.example/extensions/1",
      "type": "ScribeExtensionService",
      "profile": "https://scribe.example/profiles/extension",
      "scribe:counter": 9007199254740993
    }]},
    "target": "https://images.example/canvas/1#t=1,2&xywh=pixel:1,2,30,10&track=audio"
  }]
}`)
	identity := iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   42,
		CanvasURI:     "https://images.example/canvas/1",
	}
	normalized, err := iiif.NormalizeAnnotationPage(raw, identity)
	if err != nil {
		t.Fatalf("NormalizeAnnotationPage: %v", err)
	}
	if err := iiif.ValidateCanonicalAnnotationPage(normalized, identity); err != nil {
		t.Fatalf("ValidateCanonicalAnnotationPage: %v", err)
	}
	if !strings.Contains(string(normalized), `"scribe:counter":9007199254740993`) {
		t.Fatalf("large extension integer changed: %s", normalized)
	}
	var document map[string]any
	if err := iiif.DecodeJSON(normalized, &document); err != nil {
		t.Fatal(err)
	}
	items := document["items"].([]any)
	body := items[0].(map[string]any)["body"].(map[string]any)
	services := body["service"].([]any)
	counter := services[0].(map[string]any)["scribe:counter"]
	if !reflect.DeepEqual(counter, json.Number("9007199254740993")) {
		t.Fatalf("counter = %#v (%T)", counter, counter)
	}
}
