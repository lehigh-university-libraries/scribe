package iiif

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildPresentationResourcesOwnsChildrenAndPreservesImportedCanvas(t *testing.T) {
	t.Parallel()

	const (
		base         = "https://scribe.example"
		sourceCanvas = "https://source.example/canvas/1"
		imageID      = "https://images.example/scan.jpg"
	)
	source := []byte(`{
  "@context":["https://example.org/iiif-extension/context.json","http://iiif.io/api/presentation/3/context.json"],
  "id":"https://source.example/manifest",
  "type":"Manifest",
  "label":{"en":["Source label"],"fr":["Libellé source"]},
  "rights":"http://creativecommons.org/licenses/by/4.0/",
  "behavior":["paged"],
  "ex:manifest":"kept",
  "items":[{
    "id":"https://source.example/canvas/1",
    "type":"Canvas",
    "label":{"en":["Source Canvas"],"fr":["Page source"]},
    "metadata":[{"label":{"en":["Shelfmark"]},"value":{"none":["MS 1"]}}],
    "ex:canvas":{"largeInteger":9007199254740993},
    "annotations":[{"id":"https://source.example/page/comments","type":"AnnotationPage","ex:workflow":{"state":"imported"}}],
    "height":1000,
    "width":800,
    "items":[{"id":"https://source.example/page","type":"AnnotationPage","items":[{
      "id":"https://source.example/painting","type":"Annotation","motivation":"painting",
      "target":"https://source.example/canvas/1",
      "body":{"type":"Choice","items":[
        {"id":"https://images.example/audio.mp3","type":"Sound","format":"audio/mpeg"},
        {"id":"https://images.example/scan.jpg","type":"Image","format":"image/jpeg","ex:quality":"source-original",
         "service":[{"id":"https://images.example/iiif/3/scan","type":"ImageService3","profile":"level2"}]}
      ]}
    }]}]
  }],
  "structures":[{"id":"https://source.example/range","type":"Range","label":{"en":["Chapter"]},"items":[{"id":"https://source.example/canvas/1","type":"Canvas"}]}]
}`)
	if err := ValidateSourceManifest(source); err != nil {
		t.Fatalf("ValidateSourceManifest: %v", err)
	}
	manifestSource, err := ParseManifestSource(source)
	if err != nil {
		t.Fatalf("ParseManifestSource: %v", err)
	}
	var retainedSource map[string]any
	if err := DecodeJSON(source, &retainedSource); err != nil || retainedSource["ex:manifest"] != "kept" {
		t.Fatalf("raw source extension was not retained for persistence: %#v/%v", retainedSource, err)
	}

	resources, err := BuildCanvasResources(CanvasBuildInput{
		PublicBaseURL: base, ItemImageID: 42, CanvasID: sourceCanvas,
		Width: 800, Height: 1000, Label: "fallback", AnnotationPageID: base + "/item-image-42/canvas/page-1/annotations",
		ImageBody: map[string]any{
			"id": imageID, "type": "Image", "format": "image/jpeg", "width": 800, "height": 1000,
			"service": []any{map[string]any{"id": "https://images.example/iiif/3/scan-inferred", "type": "ImageService3", "profile": "level0"}},
		},
		ManifestSource: manifestSource,
	})
	if err != nil {
		t.Fatalf("BuildCanvasResources: %v", err)
	}
	canvasBytes, _ := json.Marshal(resources.Canvas)
	if err := ValidateCanvas(canvasBytes); err != nil {
		t.Fatalf("ValidateCanvas: %v", err)
	}
	pageBytes, _ := json.Marshal(resources.PaintingPage)
	if err := ValidateAnnotationPage(pageBytes); err != nil {
		t.Fatalf("validate painting AnnotationPage: %v", err)
	}
	annotationBytes, _ := json.Marshal(resources.PaintingAnnotation)
	if err := ValidateAnnotation(annotationBytes); err != nil {
		t.Fatalf("ValidateAnnotation: %v", err)
	}

	if got := resourceStringValue(resources.Canvas, "id"); got != sourceCanvas {
		t.Fatalf("Canvas id = %q, want imported target/provenance %q", got, sourceCanvas)
	}
	if got := resourceStringValue(resources.PaintingAnnotation, "target"); got != sourceCanvas {
		t.Fatalf("painting target = %q, want imported Canvas", got)
	}
	wantPage, _ := PaintingPageID(base, 42)
	wantAnnotation, _ := PaintingAnnotationID(base, 42)
	if resourceStringValue(resources.PaintingPage, "id") != wantPage || resourceStringValue(resources.PaintingAnnotation, "id") != wantAnnotation {
		t.Fatalf("painting resources do not use Scribe-owned route ids")
	}
	for name, resource := range map[string]map[string]any{
		"Canvas": resources.Canvas, "painting AnnotationPage": resources.PaintingPage, "painting Annotation": resources.PaintingAnnotation,
	} {
		contexts, ok := resource["@context"].([]any)
		if !ok || !containsString(contexts, "https://example.org/iiif-extension/context.json") || contexts[len(contexts)-1] != PresentationContext {
			t.Fatalf("%s did not preserve the source extension context before the Presentation context: %#v", name, resource["@context"])
		}
	}
	if _, ok := resources.Canvas["metadata"]; !ok {
		t.Fatal("source Canvas metadata was not preserved")
	}
	sourceAnnotationPage := resources.Canvas["annotations"].([]any)[0].(map[string]any)
	if sourceAnnotationPage["ex:workflow"].(map[string]any)["state"] != "imported" {
		t.Fatalf("source Canvas AnnotationPage extension was not preserved: %#v", sourceAnnotationPage)
	}
	if got := resources.Canvas["ex:canvas"].(map[string]any)["largeInteger"].(json.Number).String(); got != "9007199254740993" {
		t.Fatalf("extension integer = %s", got)
	}
	body := resources.PaintingAnnotation["body"].(map[string]any)
	if body["ex:quality"] != "source-original" {
		t.Fatalf("selected source Image extension was not preserved: %#v", body)
	}
	services := body["service"].([]any)
	if got := resourceStringValue(services[0].(map[string]any), "id"); got != "https://images.example/iiif/3/scan" {
		t.Fatalf("source Image API service was replaced by URL inference: %#v", services)
	}
	manifestBytes, err := BuildManifest(manifestSource, base+"/v1/items/item-1/manifest", "fallback", []any{EmbeddedResource(resources.Canvas)}, true)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if err := ValidateManifest(manifestBytes); err != nil {
		t.Fatalf("ValidateManifest: %v", err)
	}
	var manifest map[string]any
	if err := DecodeJSON(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["rights"] == nil || manifest["behavior"] == nil {
		t.Fatalf("source Manifest properties were not preserved: %#v", manifest)
	}
	if manifest["ex:manifest"] != "kept" {
		t.Fatalf("source Manifest extension property was not preserved: %#v", manifest)
	}
	label := manifest["label"].(map[string]any)
	if label["fr"].([]any)[0] != "Libellé source" {
		t.Fatalf("multilingual label was not preserved: %#v", label)
	}
	rangeItems := manifest["structures"].([]any)[0].(map[string]any)["items"].([]any)
	if got := resourceStringValue(rangeItems[0].(map[string]any), "id"); got != sourceCanvas {
		t.Fatalf("Range Canvas id = %q, want imported %q", got, sourceCanvas)
	}

	partial, err := BuildManifest(manifestSource, base+"/v1/items/item-1/manifest", "fallback", []any{EmbeddedResource(resources.Canvas)}, false)
	if err != nil {
		t.Fatal(err)
	}
	var partialManifest map[string]any
	_ = DecodeJSON(partial, &partialManifest)
	if _, leaked := partialManifest["structures"]; leaked {
		t.Fatal("partial publication retained ranges that may reference unpublished Canvases")
	}
}

func TestPartialManifestDropsStartOutsideEmittedCanvasSet(t *testing.T) {
	t.Parallel()
	const (
		selectedCanvas = "https://source.example/canvas/selected"
		otherCanvas    = "https://source.example/canvas/other"
	)
	source, err := ParseManifestSource([]byte(`{
  "type":"Manifest",
  "start":{"id":"https://source.example/canvas/other","type":"Canvas"},
  "items":[
    {"id":"https://source.example/canvas/selected","type":"Canvas"},
    {"id":"https://source.example/canvas/other","type":"Canvas"}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	canvas := map[string]any{
		"id": selectedCanvas, "type": "Canvas", "width": json.Number("10"), "height": json.Number("10"),
		"items": []any{map[string]any{
			"id": "https://scribe.example/painting", "type": "AnnotationPage", "items": []any{map[string]any{
				"id": "https://scribe.example/painting/image", "type": "Annotation", "motivation": "painting",
				"target": selectedCanvas, "body": map[string]any{"id": "https://images.example/image.jpg", "type": "Image", "format": "image/jpeg"},
			}},
		}},
	}
	manifestBytes, err := BuildManifest(source, "https://scribe.example/manifest", "Selected", []any{canvas}, false)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := DecodeJSON(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if _, leaked := manifest["start"]; leaked {
		t.Fatalf("partial Manifest retained start Canvas %q outside emitted items", otherCanvas)
	}

	source.manifest["start"] = map[string]any{"id": selectedCanvas, "type": "Canvas"}
	manifestBytes, err = BuildManifest(source, "https://scribe.example/manifest", "Selected", []any{canvas}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := DecodeJSON(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if resourceStringValue(manifest["start"].(map[string]any), "id") != selectedCanvas {
		t.Fatal("partial Manifest dropped a start Canvas that is present in emitted items")
	}
}

func TestTextGranularityTermsAreCaseSensitive(t *testing.T) {
	t.Parallel()
	if !IsTextGranularity(" word ") {
		t.Fatal("trimmed canonical Text Granularity term was rejected")
	}
	if IsTextGranularity("WORD") {
		t.Fatal("case-invalid Text Granularity term was accepted")
	}
}

func TestAnnotationFromPagePreservesGenericAndTextGranularityProperties(t *testing.T) {
	t.Parallel()
	identity := PageIdentity{PublicBaseURL: "https://scribe.example", ItemImageID: 7, CanvasURI: "https://scribe.example/item-image-7/canvas/page-1"}
	pageID, _ := AnnotationPageID(identity.PublicBaseURL, identity.ItemImageID)
	annotationID, _ := AnnotationID(pageID, "line")
	page, err := NewAnnotationPage(identity, []any{map[string]any{
		"id": annotationID, "type": "Annotation", "motivation": "supplementing", "textGranularity": "line", "ex:confidence": json.Number("0.875"),
		"body":   map[string]any{"type": "TextualBody", "value": "hello", "format": "text/plain"},
		"target": map[string]any{"type": "SpecificResource", "source": identity.CanvasURI, "selector": map[string]any{"type": "FragmentSelector", "conformsTo": "http://www.w3.org/TR/media-frags/", "value": "xywh=1,2,30,40"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := AnnotationFromPage(page, annotationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAnnotation(child); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(child), `"ex:confidence":0.875`) {
		t.Fatalf("standalone child lost extension property: %s", child)
	}
	children, err := AnnotationsFromPage(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].ID != annotationID || string(children[0].Payload) != string(child) {
		t.Fatalf("batched standalone children = %#v", children)
	}
}

func TestCanonicalResourceIDsFitPersistenceBoundary(t *testing.T) {
	t.Parallel()
	const suffix = "/item-image-18446744073709551615/canvas/page-1/annotations/items/0123456789abcdef0123456789abcdef"
	allowed := "https://example.org/" + strings.Repeat("a", 512-len(suffix)-len("https://example.org/"))
	pageID, err := AnnotationPageID(allowed, ^uint64(0))
	if err != nil {
		t.Fatalf("maximum fitting base rejected: %v", err)
	}
	childID := pageID + "/items/0123456789abcdef0123456789abcdef"
	if len(childID) != 512 {
		t.Fatalf("boundary child id length = %d, want 512", len(childID))
	}
	if _, err := AnnotationPageID(allowed+"a", ^uint64(0)); err == nil {
		t.Fatal("public base that would overflow the persisted child id was accepted")
	}
}
