package server

import "testing"

func TestExternalRequestFromHeadersUsesExplicitKey(t *testing.T) {
	req := externalRequestFromHeaders(map[string][]string{
		"X-Scribe-External-Source": {"islandora"},
	}, "event-123", "image-url", stableRequestHash("request"))

	if req.source != "islandora" {
		t.Fatalf("source = %q, want islandora", req.source)
	}
	if req.key == "" || req.key == "event-123" {
		t.Fatalf("key should be a stored hash, got %q", req.key)
	}
}

func TestExternalRequestFromHeadersUsesExplicitKeyForIslandoraEvent(t *testing.T) {
	req := externalRequestFromHeaders(map[string][]string{
		"X-Islandora-Event": {"eyJ0eXBlIjoiQWN0aXZpdHkifQ=="},
	}, "event-456", "image-url", stableRequestHash("request"))

	if req.source != "islandora" {
		t.Fatalf("source = %q, want islandora", req.source)
	}
	if req.key == "" {
		t.Fatal("expected hashed explicit key")
	}
	if req.eventHeader == "" {
		t.Fatal("expected event header to be retained")
	}
}
