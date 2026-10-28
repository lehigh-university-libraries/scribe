// Package ocrcatalog exposes the server-owned model artifact catalog.
package ocrcatalog

import (
	_ "embed"
	"maps"

	"gopkg.in/yaml.v3"
)

//go:embed ocr.yaml
var data []byte

// RecognitionModel is an immutable Kraken recognition artifact.
type RecognitionModel struct {
	File   string `yaml:"file"`
	DOI    string `yaml:"doi"`
	SHA256 string `yaml:"sha256"`
}

// RecognitionModels returns an independent copy of the trusted model registry.
func RecognitionModels() map[string]RecognitionModel { return maps.Clone(recognitionModels) }

var recognitionModels = parseRecognitionModels()

type LayoutModel struct {
	Label       string `yaml:"label"`
	Granularity string `yaml:"granularity"`
	Alias       string `yaml:"alias"`
}

func LayoutModels() map[string]LayoutModel { return maps.Clone(layoutCatalog.Models) }
func LayoutModelDir() string               { return layoutCatalog.ModelDir }

type layoutCatalogData struct {
	ModelDir string                 `yaml:"model_dir"`
	Models   map[string]LayoutModel `yaml:"models"`
}

var layoutCatalog = parseLayoutCatalog()

func parseLayoutCatalog() layoutCatalogData {
	var catalog struct {
		Layout layoutCatalogData `yaml:"layout"`
	}
	if err := yaml.Unmarshal(data, &catalog); err != nil {
		panic(err)
	}
	return catalog.Layout
}

func parseRecognitionModels() map[string]RecognitionModel {
	var catalog struct {
		Kraken struct {
			Models map[string]RecognitionModel `yaml:"recognition_models"`
		} `yaml:"kraken"`
	}
	if err := yaml.Unmarshal(data, &catalog); err != nil {
		panic(err)
	}
	return catalog.Kraken.Models
}
