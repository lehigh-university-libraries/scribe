package deployer

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type runtimeOverride struct {
	environment string
	terraform   string
	minimum     uint64
	maximum     uint64
	durationMin time.Duration
	durationMax time.Duration
}

var (
	durationOverridePattern = regexp.MustCompile(`^[1-9][0-9]*(s|m|h)$`)
	runtimeOverrides        = []runtimeOverride{
		{environment: "IIIF_MAX_MANIFEST_CANVASES", terraform: "iiif_max_manifest_canvases", minimum: 1, maximum: 5_000},
		{environment: "IIIF_MAX_MANIFEST_IMPORT_BYTES", terraform: "iiif_max_manifest_import_bytes", minimum: 1, maximum: 64 << 20},
		{environment: "STORAGE_MAX_BYTES_PER_WORKSPACE", terraform: "storage_max_bytes_per_workspace", minimum: 100 << 20, maximum: 10 << 40},
		{environment: "STORAGE_MAX_BYTES_TOTAL", terraform: "storage_max_bytes_total", minimum: 100 << 20, maximum: 10 << 40},
		{environment: "STORAGE_MAX_IMAGES_PER_WORKSPACE", terraform: "storage_max_images_per_workspace", minimum: 1, maximum: 10_000_000},
		{environment: "STORAGE_MAX_IMAGES_TOTAL", terraform: "storage_max_images_total", minimum: 1, maximum: 10_000_000},
		{environment: "STORAGE_MAX_ITEMS_PER_WORKSPACE", terraform: "storage_max_items_per_workspace", minimum: 1, maximum: 10_000_000},
		{environment: "STORAGE_MAX_ITEMS_TOTAL", terraform: "storage_max_items_total", minimum: 1, maximum: 10_000_000},
		{environment: "STORAGE_NORMALIZATION_CACHE_MAX_AGE", terraform: "storage_normalization_cache_max_age", durationMin: time.Hour, durationMax: 365 * 24 * time.Hour},
		{environment: "STORAGE_NORMALIZATION_CACHE_MAX_BYTES", terraform: "storage_normalization_cache_max_bytes", minimum: 100 << 20, maximum: 10 << 40},
		{environment: "STORAGE_RESERVATION_TTL", terraform: "storage_reservation_ttl", durationMin: 5 * time.Minute, durationMax: 24 * time.Hour},
		{environment: "TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE", terraform: "transcription_max_active_jobs_per_workspace", minimum: 1, maximum: 100_000},
	}
)

// WriteRuntimeOverrides translates the fixed set of protected GitHub
// variables into Terraform's environment-variable form. Empty values are
// deliberately omitted so config.yaml remains the single default owner.
func WriteRuntimeOverrides(writer io.Writer, getenv func(string) string) error {
	if writer == nil || getenv == nil {
		return errors.New("runtime override output is not configured")
	}
	for _, override := range runtimeOverrides {
		value := getenv(override.environment)
		if value == "" {
			continue
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must be a single-line Terraform value", override.environment)
		}
		if override.durationMin > 0 {
			if !durationOverridePattern.MatchString(value) {
				return fmt.Errorf("%s must be a positive duration using s, m, or h", override.environment)
			}
			parsed, err := time.ParseDuration(value)
			if err != nil || parsed < override.durationMin || parsed > override.durationMax {
				return fmt.Errorf("%s must be between %s and %s", override.environment, override.durationMin, override.durationMax)
			}
		} else {
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil || parsed < override.minimum || parsed > override.maximum {
				return fmt.Errorf("%s must be an integer between %d and %d", override.environment, override.minimum, override.maximum)
			}
		}
		if _, err := fmt.Fprintf(writer, "TF_VAR_%s=%s\n", override.terraform, value); err != nil {
			return errors.New("write runtime override: failed")
		}
	}
	return nil
}
