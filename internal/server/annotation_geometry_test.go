package server

import (
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
)

func TestStructuralBaselineSplitAndJoin(t *testing.T) {
	line := structuralAnnotation(t, "geometry-line", "line", "one two three four", 10, 20, 400, 40)
	replaceLineGeometry(line, []ocrpage.Point{{X: 10, Y: 20}, {X: 410, Y: 20}, {X: 410, Y: 60}, {X: 10, Y: 60}}, []ocrpage.Point{{X: 10, Y: 50}, {X: 410, Y: 54}})
	draft := canonicalAnnotationDraft(t, nil, line)
	if err := splitDraftLineIntoTwo(draft, annStringValue(line, "id"), 2); err != nil {
		t.Fatal(err)
	}
	assertCanonicalDraft(t, draft)
	lines := draftAnnotationsByGranularity(draft, "line")
	if len(lines) != 2 {
		t.Fatalf("split %d lines", len(lines))
	}
	first, err := annotationLineGeometry(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := annotationLineGeometry(lines[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Baseline) != 2 || len(second.Baseline) != 2 || first.Baseline[1].X != 210 || second.Baseline[0].X != 210 || first.Baseline[0].Y >= 40 || second.Baseline[0].Y < 40 {
		t.Fatalf("split baselines %v %v", first.Baseline, second.Baseline)
	}
	draft.reindex() // The next structural RPC normally decodes and indexes the draft anew.
	if err := joinDraftLines(draft, []string{annStringValue(lines[0], "id"), annStringValue(lines[1], "id")}); err != nil {
		t.Fatal(err)
	}
	assertCanonicalDraft(t, draft)
	merged, err := annotationLineGeometry(draftAnnotationsByGranularity(draft, "line")[0])
	if err != nil || len(merged.Baseline) != 4 || len(merged.Polygon) != 0 {
		t.Fatalf("merged geometry %+v %v", merged, err)
	}
}

func TestTargetResizeTransformsSVGPointsAndPreservesProperties(t *testing.T) {
	line := structuralAnnotation(t, "move-geometry", "line", "checked", 10, 20, 100, 40)
	replaceLineGeometry(line, nil, []ocrpage.Point{{X: 10, Y: 50}, {X: 110, Y: 55}})
	target, err := targetWithFragment(line["target"], testCanvas, 20, 40, 220, 120)
	if err != nil {
		t.Fatal(err)
	}
	selectors := target["selector"].([]any)
	if got := selectors[1].(map[string]any)["value"].(string); !strings.Contains(got, `points="20,100 220,110"`) {
		t.Fatalf("SVG resize %s", got)
	}
}
