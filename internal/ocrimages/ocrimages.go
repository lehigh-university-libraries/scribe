// Package ocrimages derives the OCR Cloud Run image build matrix from
// config/ocr.yaml and resolves it to digest-pinned images for Terraform.
//
// Service names must stay in sync with terraform/kraken.tf and
// terraform/ollama.tf, which compute the same names from the same keys.
package ocrimages

import (
	"crypto/md5" // #nosec G501 -- non-cryptographic name hash shared with Terraform's md5().
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the subset of config/ocr.yaml that determines OCR images.
type Config struct {
	Kraken struct {
		PipSpec                  string           `yaml:"pip_spec"`
		DefaultSegmentationModel string           `yaml:"default_segmentation_model"`
		SegmentationModels       map[string]Model `yaml:"segmentation_models"`
	} `yaml:"kraken"`
	Ollama struct {
		BaseImage    string                 `yaml:"base_image"`
		DefaultModel string                 `yaml:"default_model"`
		Models       map[string]OllamaModel `yaml:"models"`
	} `yaml:"ollama"`
}

// Model is one Kraken model artifact.
type Model struct {
	File   string `yaml:"file"`
	DOI    string `yaml:"doi"`
	SHA256 string `yaml:"sha256"`
}

// OllamaModel pins one Ollama model manifest.
type OllamaModel struct {
	ManifestDigest string `yaml:"manifest_digest"`
}

// Entry is one buildable OCR image. JSON field names are consumed by the
// build-ocr GitHub Actions matrix.
type Entry struct {
	Key         string `json:"key"`
	ServiceName string `json:"service_name"`
	GARImage    string `json:"gar_image"`
	Image       string `json:"image"`
	Context     string `json:"context"`
	File        string `json:"file"`
	Platform    string `json:"platform"`
	BuildArgs   string `json:"build_args"`
}

var (
	pipSpecPattern    = regexp.MustCompile(`^kraken==[0-9]+\.[0-9]+(\.[0-9]+)?$`)
	modelKeyPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	modelFilePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.mlmodel$`)
	doiPattern        = regexp.MustCompile(`^10\.[0-9]{4,9}/[A-Za-z0-9._:/+()-]+$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	baseImagePattern  = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)
	tagPattern        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	nonAlphanumerics  = regexp.MustCompile(`[^a-z0-9]+`)
	reservedSegmentID = map[string]bool{"auto": true, "custom": true, "scribe": true, "tesseract": true}
)

// Load reads and parses an OCR config file.
func Load(path string) (Config, error) {
	// #nosec G304 -- the path is the repository's OCR config, chosen by the operator.
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Matrix validates cfg and returns every OCR image, tagged with tag, in the
// Artifact Registry repository garRepo (for example
// us-docker.pkg.dev/project/internal).
func Matrix(cfg Config, garRepo, tag string) ([]Entry, error) {
	if !tagPattern.MatchString(tag) {
		return nil, fmt.Errorf("image tag %q is not a valid Docker tag", tag)
	}
	k := cfg.Kraken
	pip := k.PipSpec
	if pip == "" {
		pip = "kraken==7.0.2"
	}
	if !pipSpecPattern.MatchString(pip) {
		return nil, fmt.Errorf("kraken.pip_spec must pin an exact kraken release: %s", pip)
	}

	segKey := k.DefaultSegmentationModel
	if segKey == "" {
		keys := sortedKeys(k.SegmentationModels)
		if len(keys) == 0 {
			return nil, fmt.Errorf("kraken.segmentation_models must declare at least one model")
		}
		segKey = keys[0]
	}
	if err := validateSegmentationKey("default segmentation model", segKey); err != nil {
		return nil, err
	}
	seg, ok := k.SegmentationModels[segKey]
	if !ok {
		return nil, fmt.Errorf("kraken.default_segmentation_model must reference a key in kraken.segmentation_models")
	}
	if err := validateModel("default segmentation model", seg); err != nil {
		return nil, err
	}
	image := func(service string) (string, string) {
		repo := garRepo + "/" + service
		return repo, repo + ":" + tag
	}
	kraken := func(key, service string, args ...string) Entry {
		repo, img := image(service)
		return Entry{
			Key: key, ServiceName: service, GARImage: repo, Image: img,
			Context: ".", File: "Dockerfile.segmentor", Platform: "linux/amd64",
			BuildArgs: strings.Join(append([]string{"KRAKEN_PIP_SPEC=" + pip}, args...), "\n") + "\n",
		}
	}
	segArgs := func(key string, m Model) []string {
		return []string{
			"KRAKEN_SEGMENTATION_MODEL_ID=" + key,
			"KRAKEN_SEGMENTATION_MODEL_DOI=" + m.DOI,
			"KRAKEN_SEGMENTATION_MODEL_FILE=" + m.File,
			"KRAKEN_SEGMENTATION_MODEL_SHA256=" + m.SHA256,
		}
	}
	entries := []Entry{kraken("segmentor", "scribe-segmentor", segArgs(segKey, seg)...)}
	seen := map[string]bool{}
	for _, key := range sortedKeys(k.SegmentationModels) {
		if err := validateSegmentationKey("segmentation model", key); err != nil {
			return nil, err
		}
		if seen[strings.ToLower(key)] {
			return nil, fmt.Errorf("case-insensitive duplicate segmentation model ID: %s", key)
		}
		seen[strings.ToLower(key)] = true
		m := k.SegmentationModels[key]
		if err := validateModel("segmentation model "+key, m); err != nil {
			return nil, err
		}
		if key != segKey {
			entries = append(entries, kraken("kraken-seg/"+key, "scribe-ks-"+hash8(key), segArgs(key, m)...))
		}
	}

	o := cfg.Ollama
	if !baseImagePattern.MatchString(o.BaseImage) {
		return nil, fmt.Errorf("ollama.base_image must use an immutable sha256 digest: %s", o.BaseImage)
	}
	if _, ok := o.Models[o.DefaultModel]; o.DefaultModel == "" || !ok {
		return nil, fmt.Errorf("ollama.default_model must reference a key in ollama.models")
	}
	for _, model := range sortedKeys(o.Models) {
		digest := o.Models[model].ManifestDigest
		if !sha256Pattern.MatchString(digest) {
			return nil, fmt.Errorf("ollama model %s must declare an exact manifest_digest", model)
		}
		service := OllamaServiceName(model)
		repo, img := image(service)
		entries = append(entries, Entry{
			Key: "ollama/" + model, ServiceName: service, GARImage: repo, Image: img,
			Context: "terraform/modules/ollama-cloud-run/image",
			File:    "terraform/modules/ollama-cloud-run/image/Dockerfile", Platform: "linux/amd64",
			BuildArgs: strings.Join([]string{
				"OLLAMA_BASE_IMAGE=" + o.BaseImage,
				"OLLAMA_MODEL=" + model,
				"OLLAMA_MODEL_DIGEST=" + digest,
			}, "\n") + "\n",
		})
	}
	return entries, nil
}

// OllamaServiceName mirrors terraform/ollama.tf's service name for model.
func OllamaServiceName(model string) string {
	slug := strings.Trim(nonAlphanumerics.ReplaceAllString(strings.ToLower(strings.TrimSpace(model)), "-"), "-")
	service := "ollama-" + slug
	if len(service) > 63 {
		service = strings.TrimRight(service[:63], "-")
	}
	return service
}

func hash8(key string) string {
	sum := md5.Sum([]byte(key)) // #nosec G401 -- naming hash, see import comment.
	return hex.EncodeToString(sum[:])[:8]
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validateModelKey(label, key string) error {
	if !modelKeyPattern.MatchString(key) || strings.Contains(key, "..") {
		return fmt.Errorf("%s must be a safe model key: %q", label, key)
	}
	return nil
}

func validateSegmentationKey(label, key string) error {
	if err := validateModelKey(label, key); err != nil {
		return err
	}
	lower := strings.ToLower(key)
	if reservedSegmentID[lower] {
		return fmt.Errorf("%s conflicts with a built-in segmentation selection: %s", label, key)
	}
	if lower == "kraken" && key != "kraken" {
		return fmt.Errorf("%s must use the canonical built-in ID 'kraken': %s", label, key)
	}
	return nil
}

func validateModel(label string, m Model) error {
	switch {
	case m.DOI == "":
		return fmt.Errorf("%s must declare a DOI", label)
	case !modelFilePattern.MatchString(m.File) || strings.Contains(m.File, ".."):
		return fmt.Errorf("%s file must be an exact .mlmodel basename: %s", label, m.File)
	case !doiPattern.MatchString(m.DOI):
		return fmt.Errorf("%s DOI is invalid: %s", label, m.DOI)
	case !sha256Pattern.MatchString(m.SHA256):
		return fmt.Errorf("%s must declare the lowercase SHA-256 digest of its DOI artifact", label)
	}
	return nil
}
