package iiif_test

import (
	"encoding/json"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func TestNewImageInformationV3UsesGeneratedLibOpsContract(t *testing.T) {
	t.Parallel()

	raw, err := iiif.NewImageInformationV3(iiif.ImageInformationV3{
		ID:             "https://images.example/iiif/3/page-1",
		Width:          320,
		Height:         240,
		Profile:        iiif.ImageProfileLevel2V3,
		ExtraQualities: []string{"color", "gray"},
	})
	if err != nil {
		t.Fatalf("NewImageInformationV3: %v", err)
	}
	if err := iiif.ValidateImageInformationV3(raw); err != nil {
		t.Fatalf("ValidateImageInformationV3: %v", err)
	}

	var document struct {
		Context string   `json:"@context"`
		Profile string   `json:"profile"`
		Quality []string `json:"extraQualities"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode info.json: %v", err)
	}
	var properties map[string]any
	if err := json.Unmarshal(raw, &properties); err != nil {
		t.Fatalf("decode info.json properties: %v", err)
	}
	if _, leaked := properties["AdditionalProperties"]; leaked {
		t.Fatalf("generated catch-all implementation detail leaked into info.json: %s", raw)
	}
	if document.Context != iiif.ImageContextV3 || document.Profile != "level2" {
		t.Fatalf("unexpected generated info.json contract: %#v", document)
	}
	if len(document.Quality) != 2 || document.Quality[0] != "color" || document.Quality[1] != "gray" {
		t.Fatalf("extraQualities = %#v", document.Quality)
	}
}

func TestNewImageInformationV3RejectsInvalidIdentityAndProfile(t *testing.T) {
	t.Parallel()

	for name, info := range map[string]iiif.ImageInformationV3{
		"relative id": {
			ID: "image/1", Width: 1, Height: 1, Profile: iiif.ImageProfileLevel0V3,
		},
		"query id": {
			ID: "https://images.example/iiif/3/1?token=secret", Width: 1, Height: 1, Profile: iiif.ImageProfileLevel0V3,
		},
		"zero dimension": {
			ID: "https://images.example/iiif/3/1", Width: 0, Height: 1, Profile: iiif.ImageProfileLevel0V3,
		},
		"unknown profile": {
			ID: "https://images.example/iiif/3/1", Width: 1, Height: 1, Profile: iiif.ImageProfileV3("level9"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := iiif.NewImageInformationV3(info); err == nil {
				t.Fatal("NewImageInformationV3 unexpectedly succeeded")
			}
		})
	}
}

func TestImageProfileDocumentV3(t *testing.T) {
	t.Parallel()

	got, err := iiif.ImageProfileDocumentV3(iiif.ImageProfileLevel2V3)
	if err != nil {
		t.Fatalf("ImageProfileDocumentV3: %v", err)
	}
	if got != "http://iiif.io/api/image/3/level2.json" {
		t.Fatalf("profile document = %q", got)
	}
}
