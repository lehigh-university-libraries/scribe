package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func TestBuildImageBodyAdvertisesTruthfulIIIFServiceProfiles(t *testing.T) {
	t.Parallel()
	localName := strings.Repeat("a", 64) + "-12345678-1234-4123-8123-123456789abc.png"
	localSource := "https://api.internal.example/static/uploads/" + localName

	tests := []struct {
		name           string
		imageURL       string
		sourceBase     string
		imageBase      string
		wantType       string
		wantProfile    string
		wantServiceURL string
		wantFormat     string
	}{
		{
			name:           "scribe image api 3",
			imageURL:       "/static/uploads/" + localName,
			sourceBase:     "https://api.internal.example",
			imageBase:      "https://images.example/iiif/3",
			wantType:       iiif.ImageServiceTypeV3,
			wantProfile:    string(iiif.ImageProfileLevel2V3),
			wantServiceURL: "https://images.example/iiif/3/" + url.PathEscape(localSource),
			wantFormat:     "image/jpeg",
		},
		{
			name:           "external image api 3 has conservative profile",
			imageURL:       "https://external.example/iiif/3/page/full/max/0/default.png",
			imageBase:      "https://images.example/iiif/3",
			wantType:       iiif.ImageServiceTypeV3,
			wantProfile:    string(iiif.ImageProfileLevel0V3),
			wantServiceURL: "https://external.example/iiif/3/page",
			wantFormat:     "image/png",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			body := buildImageBody(test.imageURL, test.sourceBase, test.imageBase, 800, 600)
			services, ok := body["service"].([]any)
			if !ok || len(services) != 1 {
				t.Fatalf("service = %#v, want one descriptor", body["service"])
			}
			service, ok := services[0].(map[string]any)
			if !ok {
				t.Fatalf("service descriptor = %#v", services[0])
			}
			if service["id"] != test.wantServiceURL || service["type"] != test.wantType || service["profile"] != test.wantProfile {
				t.Fatalf("service descriptor = %#v, want id=%q type=%q profile=%q", service, test.wantServiceURL, test.wantType, test.wantProfile)
			}
			if body["format"] != test.wantFormat {
				t.Fatalf("body format = %#v, want %q", body["format"], test.wantFormat)
			}
		})
	}
}

func TestBuildImageBodyDoesNotInventUnknownExternalMediaType(t *testing.T) {
	t.Parallel()

	body := buildImageBody("https://external.example/download?id=page", "https://api.internal.example", "https://images.example/iiif/3", 800, 600)
	if _, exists := body["format"]; exists {
		t.Fatalf("body invented a media type for an extensionless resource: %#v", body)
	}
}

func TestIIIFServiceInferenceUsesOnlyAnUnsignedImageRequestPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "encoded identifier",
			raw:  "https://external.example/iiif/3/ark:%2F123/full/max/0/default.jpg",
			want: "https://external.example/iiif/3/ark:%2F123",
		},
		{
			name: "marker in query",
			raw:  "https://external.example/download.jpg?next=/iiif/3/page/full/max/0/default.jpg",
		},
		{
			name: "signed image request",
			raw:  "https://external.example/iiif/3/page/full/max/0/default.jpg?token=secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := iiifServiceFromImageURL(test.raw); got != test.want {
				t.Fatalf("iiifServiceFromImageURL(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}
