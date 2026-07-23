package iiif_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func TestNormalizeAnnotationPageProducesConformantHTTPIdentifiers(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "@context": "http://iiif.io/api/presentation/3/context.json",
  "id": "urn:legacy:page",
  "type": "AnnotationPage",
  "label": {"en": ["OCR"]},
  "items": [{
    "id": "line-1",
    "type": "Annotation",
    "motivation": "supplementing",
    "textGranularity": "line",
    "body": {"type": "TextualBody", "purpose": "supplementing", "format": "text/plain", "value": "Hello"},
    "target": "https://images.example/canvas/1#xywh=1,2,30,10"
  }]
}`)

	normalized, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   42,
		CanvasURI:     "https://images.example/canvas/1",
	})
	if err != nil {
		t.Fatalf("NormalizeAnnotationPage: %v", err)
	}
	if err := iiif.ValidateAnnotationPage(normalized); err != nil {
		t.Fatalf("ValidateAnnotationPage: %v", err)
	}

	var page struct {
		ID    string `json:"id"`
		Label any    `json:"label"`
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(normalized, &page); err != nil {
		t.Fatalf("decode normalized page: %v", err)
	}
	if page.ID != "https://scribe.example/item-image-42/canvas/page-1/annotations" {
		t.Fatalf("page id = %q", page.ID)
	}
	if len(page.Items) != 1 || !strings.HasPrefix(page.Items[0].ID, page.ID+"/items/") {
		t.Fatalf("annotation id = %q, want canonical HTTP child", page.Items[0].ID)
	}
	if page.Label == nil {
		t.Fatal("extension/presentation properties were not preserved")
	}
}

func TestNormalizeAnnotationPagePreservesPageExtensionBeyondLibopsProjection(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "@context":[
    "http://iiif.io/api/extension/text-granularity/context.json",
    "https://example.org/ocr-workflow/context.json",
    "http://iiif.io/api/presentation/3/context.json"
  ],
  "type":"AnnotationPage",
  "ex:workflow":{"state":"review","largeInteger":9007199254740993},
  "items":[{
    "type":"Annotation","motivation":"supplementing","textGranularity":"line",
    "body":{"type":"TextualBody","value":"Hello"},
    "target":"https://images.example/canvas/1#xywh=1,2,30,10"
  }]
}`)

	normalized, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   43,
		CanvasURI:     "https://images.example/canvas/1",
	})
	if err != nil {
		t.Fatalf("NormalizeAnnotationPage: %v", err)
	}
	if err := iiif.ValidateAnnotationPage(normalized); err != nil {
		t.Fatalf("ValidateAnnotationPage: %v", err)
	}
	if !strings.Contains(string(normalized), `"ex:workflow":{"largeInteger":9007199254740993,"state":"review"}`) {
		t.Fatalf("page extension did not survive normalization: %s", normalized)
	}
}

func TestNormalizeAnnotationPageRejectsUncontextedPageExtension(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "@context":"http://iiif.io/api/presentation/3/context.json",
  "type":"AnnotationPage",
  "workflow":{"state":"review"},
  "items":[]
}`)
	_, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   44,
		CanvasURI:     "https://images.example/canvas/1",
	})
	if err == nil || !strings.Contains(err.Error(), "require an extension context") {
		t.Fatalf("uncontexted page extension error = %v", err)
	}
}

func TestNormalizeAnnotationPageAssignsDistinctIDsToIdenticalItems(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "type": "AnnotationPage",
  "items": [
    {"type":"Annotation","motivation":"supplementing","body":{"type":"TextualBody","value":"same"},"target":"https://images.example/canvas/1#xywh=1,2,3,4"},
    {"type":"Annotation","motivation":"supplementing","body":{"type":"TextualBody","value":"same"},"target":"https://images.example/canvas/1#xywh=1,2,3,4"}
  ]
}`)
	normalized, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   42,
		CanvasURI:     "https://images.example/canvas/1",
	})
	if err != nil {
		t.Fatalf("NormalizeAnnotationPage: %v", err)
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(normalized, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID == page.Items[1].ID {
		t.Fatalf("normalized item IDs = %+v, want two distinct IDs", page.Items)
	}
}

func TestNormalizeAnnotationPageRekeysCanonicalLookingInvalidChildID(t *testing.T) {
	t.Parallel()
	pageID := "https://scribe.example/item-image-42/canvas/page-1/annotations"
	raw, err := json.Marshal(map[string]any{
		"type": "AnnotationPage",
		"items": []any{map[string]any{
			"id":              pageID + "/items/" + strings.Repeat("a", 600),
			"type":            "Annotation",
			"motivation":      "supplementing",
			"textGranularity": "line",
			"body":            map[string]any{"type": "TextualBody", "value": "text"},
			"target":          "https://images.example/canvas/1#xywh=1,2,30,10",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   42,
		CanvasURI:     "https://images.example/canvas/1",
	})
	if err != nil {
		t.Fatalf("NormalizeAnnotationPage: %v", err)
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(normalized, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	suffix := strings.TrimPrefix(page.Items[0].ID, pageID+"/items/")
	if len(suffix) != 32 || strings.Trim(suffix, "0123456789abcdef") != "" {
		t.Fatalf("normalized annotation id = %q, want 32-lowercase-hex child", page.Items[0].ID)
	}
}

func TestAnnotationPageAdmissionLimitsPrecedeSchemaWork(t *testing.T) {
	t.Parallel()

	oversized := make([]byte, iiif.MaxAnnotationPageBytes+1)
	if err := iiif.ValidateAnnotationPage(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized page error = %v", err)
	}

	items := make([]any, iiif.MaxAnnotationsPerPage+1)
	for index := range items {
		items[index] = map[string]any{}
	}
	raw, err := json.Marshal(map[string]any{"type": "AnnotationPage", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   42,
		CanvasURI:     "https://images.example/canvas/1",
	}); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("annotation count error = %v", err)
	}

	largeBody := map[string]any{
		"type": "AnnotationPage",
		"items": []any{map[string]any{
			"type":   "Annotation",
			"body":   map[string]any{"type": "TextualBody", "value": strings.Repeat("x", (1<<20)+1)},
			"target": "https://images.example/canvas/1#xywh=1,2,30,10",
		}},
	}
	raw, err = json.Marshal(largeBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   42,
		CanvasURI:     "https://images.example/canvas/1",
	}); err == nil || !strings.Contains(err.Error(), "body exceeds") {
		t.Fatalf("annotation body error = %v", err)
	}

	largeSelector := map[string]any{
		"type": "AnnotationPage",
		"items": []any{map[string]any{
			"type": "Annotation",
			"body": map[string]any{"type": "TextualBody", "value": "text"},
			"target": map[string]any{
				"source":   "https://images.example/canvas/1",
				"selector": map[string]any{"type": "FragmentSelector", "value": strings.Repeat("1", (4<<10)+1)},
			},
		}},
	}
	raw, err = json.Marshal(largeSelector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   42,
		CanvasURI:     "https://images.example/canvas/1",
	}); err == nil || !strings.Contains(err.Error(), "selector value exceeds") {
		t.Fatalf("annotation selector error = %v", err)
	}
}

func TestValidateAnnotationPageRejectsNonHTTPResourceID(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "@context": "http://iiif.io/api/presentation/3/context.json",
  "id": "urn:scribe:annotation-page:1",
  "type": "AnnotationPage",
  "items": []
}`)
	if err := iiif.ValidateAnnotationPage(raw); err == nil {
		t.Fatal("ValidateAnnotationPage accepted a non-HTTP resource id")
	}
}

func TestValidateAnnotationPageEnforcesTextGranularitySemantics(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "@context": [
    "http://iiif.io/api/extension/text-granularity/context.json",
    "http://iiif.io/api/presentation/3/context.json"
  ],
  "id": "https://scribe.example/v1/item-images/1/annotations",
  "type": "AnnotationPage",
  "items": [{
    "id": "https://scribe.example/v1/item-images/1/annotations/items/1",
    "type": "Annotation",
    "motivation": "commenting",
    "textGranularity": "line",
    "body": {"type": "TextualBody", "value": "Hello"},
    "target": "https://images.example/canvas/1#xywh=1,2,30,10"
  }]
}`)
	if err := iiif.ValidateAnnotationPage(raw); err == nil || !strings.Contains(err.Error(), "supplementing") {
		t.Fatalf("ValidateAnnotationPage error = %v, want supplementing semantic failure", err)
	}
}

func TestValidateAnnotationPageRejectsTextGranularityWithoutTextBody(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
  "@context": [
    "http://iiif.io/api/extension/text-granularity/context.json",
    "http://iiif.io/api/presentation/3/context.json"
  ],
  "id": "https://scribe.example/v1/item-images/1/annotations",
  "type": "AnnotationPage",
  "items": [{
    "id": "https://scribe.example/v1/item-images/1/annotations/items/missing-body",
    "type": "Annotation",
    "motivation": "supplementing",
    "textGranularity": "line",
    "target": "https://source.example/canvas/1#xywh=0,0,100,20"
  }]
}`)

	if err := iiif.ValidateAnnotationPage(raw); err == nil || !strings.Contains(err.Error(), "TextualBody") {
		t.Fatalf("ValidateAnnotationPage error = %v, want missing TextualBody semantic failure", err)
	}
}

func TestValidateAnnotationPageAcceptsTextualBodyWithoutPurpose(t *testing.T) {
	t.Parallel()

	// The Text Granularity Extension assigns supplementing to the Annotation's
	// motivation. Its normative TextualBody example does not repeat that value
	// in a body purpose property.
	raw := []byte(`{
  "@context": [
    "http://iiif.io/api/extension/text-granularity/context.json",
    "http://iiif.io/api/presentation/3/context.json"
  ],
  "id": "https://scribe.example/v1/item-images/1/annotations",
  "type": "AnnotationPage",
  "items": [{
    "id": "https://scribe.example/v1/item-images/1/annotations/items/line-1",
    "type": "Annotation",
    "motivation": ["supplementing"],
    "textGranularity": "line",
    "body": {"type": "TextualBody", "value": "Hello"},
    "target": {"type":"SpecificResource","source":"https://source.example/canvas/1","selector":{"type":"FragmentSelector","value":"xywh=1,2,30,10"}}
  }]
}`)

	if err := iiif.ValidateAnnotationPage(raw); err != nil {
		t.Fatalf("ValidateAnnotationPage rejected the extension's TextualBody pattern: %v", err)
	}
}

func TestNormalizeAnnotationPagePreservesSingleCanvasReferenceTarget(t *testing.T) {
	t.Parallel()
	identity := iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example", ItemImageID: 15,
		CanvasURI: "https://source.example/canvas/15",
	}
	raw := []byte(`{
  "@context": [
    "http://iiif.io/api/extension/text-granularity/context.json",
    "http://iiif.io/api/presentation/3/context.json"
  ],
  "type": "AnnotationPage",
  "items": [{
    "type": "Annotation",
    "motivation": "supplementing",
    "textGranularity": "page",
    "body": {"type": "TextualBody", "value": "Whole page"},
    "target": [{
      "id": "https://source.example/canvas/15",
      "type": "Canvas",
      "label": {"en": ["Canvas fifteen"]},
      "thumbnail": [{"id": "https://images.example/15-thumb.jpg", "type": "Image"}]
    }]
  }]
}`)
	normalized, err := iiif.NormalizeAnnotationPage(raw, identity)
	if err != nil {
		t.Fatalf("NormalizeAnnotationPage: %v", err)
	}
	if err := iiif.ValidateCanonicalAnnotationPage(normalized, identity); err != nil {
		t.Fatalf("ValidateCanonicalAnnotationPage: %v", err)
	}
	var page map[string]any
	if err := iiif.DecodeJSON(normalized, &page); err != nil {
		t.Fatal(err)
	}
	targets := page["items"].([]any)[0].(map[string]any)["target"].([]any)
	target := targets[0].(map[string]any)
	if target["id"] != identity.CanvasURI || target["type"] != "Canvas" || target["label"] == nil || target["thumbnail"] == nil {
		t.Fatalf("Canvas reference changed during normalization: %#v", target)
	}
}

func TestCanonicalAnnotationPageRejectsMultipleTargets(t *testing.T) {
	t.Parallel()
	identity := iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example", ItemImageID: 16,
		CanvasURI: "https://source.example/canvas/16",
	}
	raw := []byte(`{
  "@context": [
    "http://iiif.io/api/extension/text-granularity/context.json",
    "http://iiif.io/api/presentation/3/context.json"
  ],
  "type": "AnnotationPage",
  "items": [{
    "type": "Annotation",
    "motivation": "supplementing",
    "textGranularity": "page",
    "body": {"type": "TextualBody", "value": "Ambiguous"},
    "target": [
      {"id": "https://source.example/canvas/16", "type": "Canvas"},
      {"id": "https://source.example/canvas/16", "type": "Canvas"}
    ]
  }]
}`)
	if _, err := iiif.NormalizeAnnotationPage(raw, identity); err == nil || !strings.Contains(err.Error(), "targets canvas") {
		t.Fatalf("NormalizeAnnotationPage multiple target error = %v", err)
	}
}

func TestNormalizeAnnotationPageKeepsPresentationContextLast(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
  "@context": [
    "http://iiif.io/api/presentation/3/context.json",
    "https://example.test/context.json"
  ],
  "type": "AnnotationPage",
  "items": []
}`)
	normalized, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   9,
		CanvasURI:     "https://source.example/canvas/9",
	})
	if err != nil {
		t.Fatalf("NormalizeAnnotationPage: %v", err)
	}
	var page struct {
		Context []string `json:"@context"`
	}
	if err := json.Unmarshal(normalized, &page); err != nil {
		t.Fatalf("decode normalized page: %v", err)
	}
	want := []string{
		iiif.TextGranularityContext,
		"https://example.test/context.json",
		iiif.PresentationContext,
	}
	if len(page.Context) != len(want) {
		t.Fatalf("@context = %#v, want %#v", page.Context, want)
	}
	for index := range want {
		if page.Context[index] != want[index] {
			t.Fatalf("@context = %#v, want %#v", page.Context, want)
		}
	}
}

func TestPresentationValidationRejectsBaseContextThatIsNotLast(t *testing.T) {
	t.Parallel()

	page := []byte(`{
  "@context": [
    "http://iiif.io/api/presentation/3/context.json",
    "https://example.test/context.json"
  ],
  "id": "https://scribe.example/v1/item-images/1/annotations",
  "type": "AnnotationPage",
  "items": []
}`)
	if err := iiif.ValidateAnnotationPage(page); err == nil || !strings.Contains(err.Error(), "final @context entry") {
		t.Fatalf("ValidateAnnotationPage error = %v, want context-order failure", err)
	}

	manifest := []byte(`{
  "@context": [
    "http://iiif.io/api/presentation/3/context.json",
    "https://example.test/context.json"
  ],
  "id": "https://scribe.example/v1/items/1/manifest",
  "type": "Manifest",
  "label": {"none":["Document"]},
  "items": []
}`)
	if err := iiif.ValidateManifest(manifest); err == nil || !strings.Contains(err.Error(), "final @context entry") {
		t.Fatalf("ValidateManifest error = %v, want context-order failure", err)
	}
}

func TestValidateAnnotationPageEnforcesCanonicalOCRGeometry(t *testing.T) {
	t.Parallel()

	page := `{
  "@context": [
    "http://iiif.io/api/extension/text-granularity/context.json",
    "http://iiif.io/api/presentation/3/context.json"
  ],
  "id": "https://scribe.example/v1/item-images/1/annotations",
  "type": "AnnotationPage",
  "items": [{
    "id": "https://scribe.example/v1/item-images/1/annotations/items/1",
    "type": "Annotation",
    "motivation": "supplementing",
    "textGranularity": "word",
    "body": {"type": "TextualBody", "purpose": "supplementing", "value": "Hello"},
    "target": "https://images.example/canvas/1#xywh=1,2,0,10"
  }]
}`
	if err := iiif.ValidateAnnotationPage([]byte(page)); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("ValidateAnnotationPage error = %v, want invalid geometry", err)
	}
}

func TestValidateAnnotationPageRequiresTextGranularityContext(t *testing.T) {
	t.Parallel()

	page := `{
  "@context": "http://iiif.io/api/presentation/3/context.json",
  "id": "https://scribe.example/v1/item-images/1/annotations",
  "type": "AnnotationPage",
  "items": [{
    "id": "https://scribe.example/v1/item-images/1/annotations/items/1",
    "type": "Annotation",
    "motivation": "supplementing",
    "textGranularity": "line",
    "body": {"type": "TextualBody", "purpose": "supplementing", "value": "Hello"},
    "target": "https://images.example/canvas/1#xywh=1,2,30,10"
  }]
}`
	if err := iiif.ValidateAnnotationPage([]byte(page)); err == nil || !strings.Contains(err.Error(), "Text Granularity context") {
		t.Fatalf("ValidateAnnotationPage error = %v, want missing extension context", err)
	}
}

func TestNormalizeAnnotationPageRejectsDifferentCanvasTarget(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "type": "AnnotationPage",
  "items": [{
    "id": "https://scribe.example/annotation/1",
    "type": "Annotation",
    "motivation": "supplementing",
    "body": {"type": "TextualBody", "value": "Hello"},
    "target": "https://source.example/canvas/other#xywh=1,2,3,4"
  }]
}`)
	_, err := iiif.NormalizeAnnotationPage(raw, iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   1,
		CanvasURI:     "https://source.example/canvas/expected",
	})
	if err == nil || !strings.Contains(err.Error(), "targets canvas") {
		t.Fatalf("NormalizeAnnotationPage error = %v, want target mismatch", err)
	}
}

func TestCanonicalPixelGeometryHasBoundedExtents(t *testing.T) {
	t.Parallel()
	page := func(fragment string) []byte {
		payload, err := json.Marshal(map[string]any{
			"type": "AnnotationPage",
			"items": []any{map[string]any{
				"type": "Annotation", "motivation": "supplementing", "textGranularity": "line",
				"body":   map[string]any{"type": "TextualBody", "purpose": "supplementing", "value": "text"},
				"target": "https://source.example/canvas/1#xywh=" + fragment,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	identity := iiif.PageIdentity{PublicBaseURL: "https://scribe.example", ItemImageID: 1, CanvasURI: "https://source.example/canvas/1"}
	for _, valid := range []string{
		"2147483646,2147483646,1,1",
		"0,1,100,200",
	} {
		if _, err := iiif.NormalizeAnnotationPage(page(valid), identity); err != nil {
			t.Errorf("valid xywh %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"0.25,1,100,200",
		"2147483647,0,1,1",
		"0,2147483647,1,1",
		"2147483648,0,1,1",
		"0,0,2147483648,1",
		"2147483646.75,0,0.5,1",
	} {
		if _, err := iiif.NormalizeAnnotationPage(page(invalid), identity); err == nil {
			t.Errorf("invalid xywh %q was accepted", invalid)
		}
	}
}

func TestAnnotationPageGeometryIsBoundToKnownImageDimensions(t *testing.T) {
	t.Parallel()
	page := []byte(`{"type":"AnnotationPage","items":[{"type":"Annotation","motivation":"supplementing","textGranularity":"line","body":{"type":"TextualBody","purpose":"supplementing","value":"text"},"target":"https://source.example/canvas/1#xywh=90,10,20,10"}]}`)
	if err := iiif.ValidateAnnotationPageGeometry(page, 100, 100); err == nil {
		t.Fatal("out-of-bounds geometry was accepted")
	}
	if err := iiif.ValidateAnnotationPageGeometry(page, 0, 0); err == nil {
		t.Fatal("geometry with unknown image dimensions was accepted")
	}
	page = []byte(`{"type":"AnnotationPage","items":[{"type":"Annotation","target":"https://source.example/canvas/1#xywh=80,10,20,10"}]}`)
	if err := iiif.ValidateAnnotationPageGeometry(page, 100, 100); err != nil {
		t.Fatalf("in-bounds geometry rejected: %v", err)
	}
}

func TestPageIdentityFromAnnotationID(t *testing.T) {
	t.Parallel()

	pageID := "https://scribe.example/app/item-image-42/canvas/page-1/annotations"
	annotationID, err := iiif.AnnotationID(pageID, "line-1")
	if err != nil {
		t.Fatalf("AnnotationID: %v", err)
	}
	identity, err := iiif.PageIdentityFromAnnotationID(
		annotationID,
		"https://source.example/canvas/1",
	)
	if err != nil {
		t.Fatalf("PageIdentityFromAnnotationID: %v", err)
	}
	if identity.PublicBaseURL != "https://scribe.example/app" || identity.ItemImageID != 42 || identity.CanvasURI != "https://source.example/canvas/1" {
		t.Fatalf("unexpected identity: %#v", identity)
	}
}

func TestPageIdentityFromAnnotationIDRejectsNonCanonicalIDs(t *testing.T) {
	t.Parallel()

	tests := []string{
		"urn:scribe:annotation:1",
		"https://scribe.example/annotations/1",
		"https://scribe.example/item-image-0/canvas/page-1/annotations/items/1",
		"https://scribe.example/item-image-1/canvas/page-1/annotations",
		"https://scribe.example/item-image-1/canvas/page-1/annotations/items/1?revision=2",
		"https://scribe.example/item-image-1/canvas/page-1/annotations/items/ABCDEF0123456789ABCDEF0123456789",
		"https://scribe.example/item-image-1/canvas/page-1/annotations/items/abcdef0123456789abcdef012345678",
	}
	for _, annotationID := range tests {
		if _, err := iiif.PageIdentityFromAnnotationID(annotationID, "https://source.example/canvas/1"); err == nil {
			t.Errorf("PageIdentityFromAnnotationID(%q) unexpectedly succeeded", annotationID)
		}
	}
}
