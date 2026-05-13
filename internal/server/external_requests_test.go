package server

import "testing"

func TestExternalRequestFromHeadersUsesExplicitKey(t *testing.T) {
	req := externalRequestFromHeaders(map[string][]string{
		"X-Idempotency-Key":        {"event-123"},
		"X-Scribe-External-Source": {"islandora"},
	}, "https://example.test/image.jpg", 7, "")

	if req.source != "islandora" {
		t.Fatalf("source = %q, want islandora", req.source)
	}
	if req.key == "" || req.key == "event-123" {
		t.Fatalf("key should be a stored hash, got %q", req.key)
	}
}

func TestExternalRequestFromHeadersDerivesIslandoraKey(t *testing.T) {
	req := externalRequestFromHeaders(map[string][]string{
		"X-Islandora-Event": {"eyJ0eXBlIjoiQWN0aXZpdHkifQ=="},
	}, "https://example.test/image.jpg", 0, "")

	if req.source != "islandora" {
		t.Fatalf("source = %q, want islandora", req.source)
	}
	if req.key == "" {
		t.Fatal("expected derived key")
	}
	if req.eventHeader == "" {
		t.Fatal("expected event header to be retained")
	}
}
