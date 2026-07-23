package iiif

import (
	"encoding/json"
	"fmt"

	imagegen "github.com/libops/iiif-spec/image/v3/gen"
	imageschema "github.com/libops/iiif-spec/image/v3/schema"
)

const (
	// ImageContextV3 is the IIIF Image API 3 JSON-LD context.
	ImageContextV3 = "http://iiif.io/api/image/3/context.json"
	// ImageProtocol is the protocol identifier shared by IIIF Image API versions.
	ImageProtocol = "http://iiif.io/api/image"
	// ImageServiceTypeV3 is the Presentation API service type for Image API 3.
	ImageServiceTypeV3 = "ImageService3"
)

// ImageProfileV3 identifies an Image API 3 compliance profile.
type ImageProfileV3 string

const (
	// ImageProfileLevel0V3 is the Image API 3 level 0 compliance profile.
	ImageProfileLevel0V3 ImageProfileV3 = ImageProfileV3(imagegen.InfoSchemaJsonProfileLevel0)
	// ImageProfileLevel1V3 is the Image API 3 level 1 compliance profile.
	ImageProfileLevel1V3 ImageProfileV3 = ImageProfileV3(imagegen.InfoSchemaJsonProfileLevel1)
	// ImageProfileLevel2V3 is the Image API 3 level 2 compliance profile.
	ImageProfileLevel2V3 ImageProfileV3 = ImageProfileV3(imagegen.InfoSchemaJsonProfileLevel2)
)

// ImageInformationV3 contains IIIF Image API capabilities represented
// by the generated iiif-spec Image API wire type.
type ImageInformationV3 struct {
	ID               string
	Width            int
	Height           int
	Profile          ImageProfileV3
	ExtraFormats     []string
	ExtraQualities   []string
	ExtraFeatures    []string
	PreferredFormats []string
}

// NewImageInformationV3 builds and validates an Image API 3 info.json document.
func NewImageInformationV3(info ImageInformationV3) ([]byte, error) {
	if err := requireResourceURL(info.ID, "image service id", false); err != nil {
		return nil, err
	}
	if info.Width <= 0 || info.Height <= 0 {
		return nil, fmt.Errorf("image service dimensions must be positive")
	}
	profile := imagegen.InfoSchemaJsonProfile(info.Profile)
	switch profile {
	case imagegen.InfoSchemaJsonProfileLevel0, imagegen.InfoSchemaJsonProfileLevel1, imagegen.InfoSchemaJsonProfileLevel2:
	default:
		return nil, fmt.Errorf("unsupported IIIF Image API 3 profile %q", info.Profile)
	}

	document := imagegen.InfoSchemaJson{
		Context:          ImageContextV3,
		Id:               info.ID,
		Type:             ImageServiceTypeV3,
		Protocol:         ImageProtocol,
		Profile:          profile,
		Width:            imagegen.PositiveInteger(info.Width),
		Height:           imagegen.PositiveInteger(info.Height),
		ExtraFormats:     imagegen.StringList(info.ExtraFormats),
		ExtraQualities:   imagegen.StringList(info.ExtraQualities),
		ExtraFeatures:    imagegen.StringList(info.ExtraFeatures),
		PreferredFormats: imagegen.StringList(info.PreferredFormats),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode IIIF Image API 3 information: %w", err)
	}
	// The generated wire type exposes its extension catch-all as a field with a
	// mapstructure tag but no json tag. encoding/json would otherwise emit the
	// generator implementation detail as "AdditionalProperties": null.
	var cleanDocument map[string]any
	if err := DecodeJSON(encoded, &cleanDocument); err != nil {
		return nil, fmt.Errorf("normalize IIIF Image API 3 information: %w", err)
	}
	delete(cleanDocument, "AdditionalProperties")
	encoded, err = json.Marshal(cleanDocument)
	if err != nil {
		return nil, fmt.Errorf("encode normalized IIIF Image API 3 information: %w", err)
	}
	if err := ValidateImageInformationV3(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

// ValidateImageInformationV3 validates an Image API 3 info.json document with
// the schema published by libops/iiif-spec.
func ValidateImageInformationV3(raw []byte) error {
	if err := imageschema.ValidateInfoBytes(raw); err != nil {
		return fmt.Errorf("invalid IIIF Image API 3 information: %w", err)
	}
	return nil
}

// ImageProfileDocumentV3 returns the profile-document URI for a compliance level.
func ImageProfileDocumentV3(profile ImageProfileV3) (string, error) {
	switch profile {
	case ImageProfileLevel0V3, ImageProfileLevel1V3, ImageProfileLevel2V3:
		return "http://iiif.io/api/image/3/" + string(profile) + ".json", nil
	default:
		return "", fmt.Errorf("unsupported IIIF Image API 3 profile %q", profile)
	}
}
