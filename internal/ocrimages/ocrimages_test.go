package ocrimages

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testRepo = "us-docker.pkg.dev/scribe-test/internal"

func loadFixture(t *testing.T) Config {
	t.Helper()
	cfg, err := Load("testdata/multi-model.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func entryByKey(t *testing.T, entries []Entry, key string) Entry {
	t.Helper()
	for _, entry := range entries {
		if entry.Key == key {
			return entry
		}
	}
	t.Fatalf("no matrix entry %q", key)
	return Entry{}
}

func hasBuildArg(entry Entry, arg string) bool {
	return slices.Contains(strings.Split(strings.TrimSuffix(entry.BuildArgs, "\n"), "\n"), arg)
}

func TestMatrixEmitsEveryService(t *testing.T) {
	entries, err := Matrix(loadFixture(t), testRepo, "main")
	if err != nil {
		t.Fatal(err)
	}
	// Generic segmentor, two segmentation models, two transcription models, one Ollama model.
	if len(entries) != 6 {
		t.Fatalf("got %d entries, want 6", len(entries))
	}

	segmentor := entryByKey(t, entries, "segmentor")
	for _, arg := range []string{"KRAKEN_SEGMENTATION_MODEL_ID=layout-default", "KRAKEN_TRANSCRIPTION_MODEL_ID=handwriting-default"} {
		if !hasBuildArg(segmentor, arg) {
			t.Errorf("segmentor missing %s", arg)
		}
	}
	if segmentor.Image != testRepo+"/scribe-segmentor:main" {
		t.Errorf("segmentor image = %s", segmentor.Image)
	}

	seg := entryByKey(t, entries, "kraken-seg/layout-lines-v2")
	if !hasBuildArg(seg, "KRAKEN_SEGMENTATION_MODEL_FILE=segmentation-engine.mlmodel") || !hasBuildArg(seg, "KRAKEN_TRANSCRIPTION_MODEL_ID=") {
		t.Errorf("segmentation image build args = %q", seg.BuildArgs)
	}

	for _, entry := range entries {
		if route, ok := strings.CutPrefix(entry.Key, "kraken-ocr/"); ok {
			if !hasBuildArg(entry, "KRAKEN_TRANSCRIPTION_MODEL_ID="+route) || !hasBuildArg(entry, "KRAKEN_SEGMENTATION_MODEL_ID=") {
				t.Errorf("%s build args = %q", entry.Key, entry.BuildArgs)
			}
		}
	}
}

// Terraform names services with substr(md5(key), 0, 8); these values must not drift.
func TestServiceNamesMatchTerraform(t *testing.T) {
	cfg, err := Load("../../config/ocr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Matrix(cfg, testRepo, "main")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"segmentor":           "scribe-segmentor",
		"ollama/glm-ocr:bf16": "ollama-glm-ocr-bf16",
	}
	for key, name := range want {
		if got := entryByKey(t, entries, key).ServiceName; got != name {
			t.Errorf("%s service = %s, want %s", key, got, name)
		}
	}
	if got := hash8("kraken"); got != "80cd46c8" {
		t.Errorf("hash8(kraken) = %s", got)
	}
}

func TestMatrixRejectsInvalidConfig(t *testing.T) {
	cases := map[string]func(*Config){
		"undeclared ollama default": func(c *Config) { c.Ollama.DefaultModel = "not-installed" },
		"missing doi": func(c *Config) {
			m := c.Kraken.TranscriptionModels["latin-handwriting-v2"]
			m.DOI = ""
			c.Kraken.TranscriptionModels["latin-handwriting-v2"] = m
		},
		"colliding default filenames": func(c *Config) {
			m := c.Kraken.SegmentationModels["layout-default"]
			m.File = "DEFAULT-TRANSCRIBER.mlmodel"
			c.Kraken.SegmentationModels["layout-default"] = m
		},
		"mutable ollama base": func(c *Config) { c.Ollama.BaseImage = "ollama/ollama:latest" },
		"unpinned kraken":     func(c *Config) { c.Kraken.PipSpec = "kraken>=7" },
		"reserved segmentation id": func(c *Config) {
			c.Kraken.SegmentationModels["auto"] = c.Kraken.SegmentationModels["layout-default"]
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := loadFixture(t)
			mutate(&cfg)
			if _, err := Matrix(cfg, testRepo, "main"); err == nil {
				t.Fatal("invalid config was accepted")
			}
		})
	}
	if _, err := Matrix(loadFixture(t), testRepo, "bad:tag"); err == nil {
		t.Fatal("invalid tag was accepted")
	}
}

func TestNondefaultImagesMayShareFilenames(t *testing.T) {
	cfg := loadFixture(t)
	m := cfg.Kraken.TranscriptionModels["latin-handwriting-v2"]
	m.File = "default-layout.mlmodel"
	cfg.Kraken.TranscriptionModels["latin-handwriting-v2"] = m
	if _, err := Matrix(cfg, testRepo, "main"); err != nil {
		t.Fatal(err)
	}
}

// The deploy-time, runtime, and embedded Ollama defaults must agree.
func TestOllamaDefaultsAgree(t *testing.T) {
	cfg, err := Load("../../config/ocr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../../config.yaml", "../../internal/config/defaults/config.yaml"} {
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatal(err)
		}
		var runtime struct {
			LLM struct {
				Ollama struct {
					Model string `yaml:"model"`
				} `yaml:"ollama"`
			} `yaml:"llm"`
		}
		if err := yaml.Unmarshal(data, &runtime); err != nil {
			t.Fatal(err)
		}
		if runtime.LLM.Ollama.Model != cfg.Ollama.DefaultModel {
			t.Errorf("%s ollama model %q != config/ocr.yaml default %q", path, runtime.LLM.Ollama.Model, cfg.Ollama.DefaultModel)
		}
	}
}
