package server

import (
	"encoding/json"
	"testing"
)

func replaceServerPageText(t *testing.T, raw, value string) string {
	t.Helper()
	var page map[string]any
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	items, _ := page["items"].([]any)
	if len(items) == 0 {
		t.Fatal("page has no annotations")
	}
	annotation, _ := items[0].(map[string]any)
	body, _ := annotation["body"].([]any)
	if len(body) == 0 {
		t.Fatal("annotation has no body")
	}
	text, _ := body[0].(map[string]any)
	text["value"] = value
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("encode page: %v", err)
	}
	return string(encoded)
}
