package server

import "testing"

func TestReadinessResponseReportsDeployedAPIImage(t *testing.T) {
	t.Setenv("SCRIBE_DEPLOYED_API_IMAGE", "ghcr.io/example/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	response := readinessResponse("ready")
	if got := response["status"]; got != "ready" {
		t.Fatalf("status = %q, want ready", got)
	}
	if got := response["api_image"]; got != "ghcr.io/example/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("api_image = %q", got)
	}
	if got := response["public_origin"]; got != "https://scribe.test" {
		t.Fatalf("public_origin = %q, want https://scribe.test", got)
	}
}

func TestReadinessResponseOmitsUnknownDeploymentImage(t *testing.T) {
	t.Setenv("SCRIBE_DEPLOYED_API_IMAGE", "")

	response := readinessResponse("not_ready")
	if _, ok := response["api_image"]; ok {
		t.Fatal("api_image is present without deployment metadata")
	}
}
