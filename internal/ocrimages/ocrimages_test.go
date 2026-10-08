package ocrimages

import (
	"strings"
	"testing"
)

const testRepo = "us-docker.pkg.dev/scribe-test/internal"

func TestMatrixBuildsOnlySegmentationAndGLMOCR(t *testing.T) {
	cfg, err := Load("../../config/ocr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Matrix(cfg, testRepo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != "segmentor" || entries[1].Key != "ollama/glm-ocr:bf16" {
		t.Fatalf("image matrix = %+v", entries)
	}
	if entries[0].ServiceName != "scribe-segmentor" || entries[1].ServiceName != "ollama-glm-ocr-bf16" {
		t.Fatalf("service names = %+v", entries)
	}
	if strings.Contains(entries[0].BuildArgs, "TRANSCRIPTION") {
		t.Fatal("segmentation image includes recognition model")
	}
	for name, mutate := range map[string]func(*Config){
		"mutable base":    func(c *Config) { c.Ollama.BaseImage = "ollama:latest" },
		"unknown default": func(c *Config) { c.Kraken.DefaultSegmentationModel = "absent" },
		"unsafe file": func(c *Config) {
			m := c.Kraken.SegmentationModels["kraken"]
			m.File = "../untrusted.mlmodel"
			c.Kraken.SegmentationModels["kraken"] = m
		},
		"missing hash": func(c *Config) {
			m := c.Kraken.SegmentationModels["kraken"]
			m.SHA256 = ""
			c.Kraken.SegmentationModels["kraken"] = m
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Load("../../config/ocr.yaml")
			if err != nil {
				t.Fatal(err)
			}
			mutate(&c)
			if _, err := Matrix(c, testRepo, "main"); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}
