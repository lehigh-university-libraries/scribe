package handlers

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/tiff"
)

func TestInvalidImageErrorsRemainClassifiableThroughWrapping(t *testing.T) {
	err := validateUploadedImageData([]byte("not an image"))
	if err == nil {
		t.Fatal("validateUploadedImageData accepted text")
	}
	if !IsInvalidImageError(err) {
		t.Fatalf("error %T is not classified as invalid image", err)
	}
	if !IsInvalidImageError(fmt.Errorf("process upload: %w", err)) {
		t.Fatal("wrapped invalid image lost its classification")
	}
	if IsInvalidImageError(errors.New("database unavailable")) {
		t.Fatal("internal failure was classified as safe client feedback")
	}
}

func TestImageConversionFailureLogRedactsRemoteDiagnostics(t *testing.T) {
	const privateDiagnostic = "PRIVATE_IMAGE_RESPONSE_https://user:token@example.test/tmp/document.jp2"

	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	logImageConversionFailure("remote image normalization failed", errors.New(privateDiagnostic), "cache_key", "safe-digest")

	if strings.Contains(logs.String(), privateDiagnostic) || strings.Contains(logs.String(), "user:token") || strings.Contains(logs.String(), "/tmp/") {
		t.Fatalf("image conversion failure log exposed remote diagnostics: %s", logs.String())
	}
	for _, metadata := range []string{
		`"msg":"remote image normalization failed"`,
		`"cache_key":"safe-digest"`,
		`"category":"internal"`,
		`"error_type":"*errors.errorString"`,
	} {
		if !strings.Contains(logs.String(), metadata) {
			t.Fatalf("image conversion failure log omitted %s: %s", metadata, logs.String())
		}
	}
}

func TestValidateUploadedImageDataRejectsUndecodableRecognizedFormats(t *testing.T) {
	t.Parallel()
	cases := map[string][]byte{
		"jpeg": {0xff, 0xd8, 0xff},
		"png":  {0x89, 'P', 'N', 'G', 13, 10, 26, 10},
		"gif":  []byte("GIF89a"),
		"webp": []byte("RIFF\x08\x00\x00\x00WEBPVP8X"),
		"tiff": {'I', 'I', 42, 0},
		"jp2":  {0xff, 0x4f, 0xff, 0x51},
	}
	for name, payload := range cases {
		name, payload := name, payload
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateUploadedImageData(payload); err == nil || !IsInvalidImageError(err) {
				t.Fatalf("signature-only %s validation = %v, want classified rejection", name, err)
			}
		})
	}
}

func TestValidateUploadedImageDataEnforcesPixelsForEveryAcceptedFormat(t *testing.T) {
	t.Parallel()
	validTIFF, oversizedTIFF := tiffFixtures(t)
	valid := map[string][]byte{
		"tiff": validTIFF,
		"webp": webPConfigFixture(2, 2),
		"jp2":  jp2ConfigFixture(2, 2),
	}
	for name, payload := range valid {
		if err := validateUploadedImageData(payload); err != nil {
			t.Fatalf("valid %s config rejected: %v", name, err)
		}
	}
	oversized := map[string][]byte{
		"tiff": oversizedTIFF,
		"webp": webPConfigFixture(65_536, 32_767),
		"jp2":  jp2ConfigFixture(20_000, 10_000),
	}
	for name, payload := range oversized {
		if err := validateUploadedImageData(payload); err == nil || !IsInvalidImageError(err) {
			t.Fatalf("oversized %s validation = %v, want classified rejection", name, err)
		}
	}
}

func webPConfigFixture(width, height int) []byte {
	width--
	height--
	return []byte{
		'R', 'I', 'F', 'F', 22, 0, 0, 0, 'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X', 10, 0, 0, 0,
		0, 0, 0, 0,
		byte(width), byte(width >> 8), byte(width >> 16),
		byte(height), byte(height >> 8), byte(height >> 16),
	}
}

func jp2ConfigFixture(width, height uint32) []byte {
	result := []byte{0, 0, 0, 12, 'j', 'P', ' ', ' ', 13, 10, 0x87, 10}
	imageHeader := make([]byte, 22)
	binary.BigEndian.PutUint32(imageHeader[0:4], uint32(len(imageHeader)))
	copy(imageHeader[4:8], "ihdr")
	binary.BigEndian.PutUint32(imageHeader[8:12], height)
	binary.BigEndian.PutUint32(imageHeader[12:16], width)
	binary.BigEndian.PutUint16(imageHeader[16:18], 3)
	imageHeader[18] = 7
	imageHeader[19] = 7
	jp2Header := make([]byte, 8, 8+len(imageHeader))
	binary.BigEndian.PutUint32(jp2Header[0:4], uint32(8+len(imageHeader)))
	copy(jp2Header[4:8], "jp2h")
	jp2Header = append(jp2Header, imageHeader...)
	return append(result, jp2Header...)
}

func tiffFixtures(t *testing.T) ([]byte, []byte) {
	t.Helper()
	var encoded bytes.Buffer
	if err := tiff.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	valid := encoded.Bytes()
	oversized := append([]byte(nil), valid...)
	if len(oversized) < 10 || string(oversized[:2]) != "II" {
		t.Fatalf("unexpected TIFF fixture header")
	}
	ifdOffset := int(binary.LittleEndian.Uint32(oversized[4:8]))
	if ifdOffset < 0 || ifdOffset+2 > len(oversized) {
		t.Fatalf("invalid TIFF IFD offset")
	}
	entryCount := int(binary.LittleEndian.Uint16(oversized[ifdOffset : ifdOffset+2]))
	changed := 0
	for index := 0; index < entryCount; index++ {
		offset := ifdOffset + 2 + index*12
		if offset+12 > len(oversized) {
			t.Fatalf("truncated TIFF IFD")
		}
		tag := binary.LittleEndian.Uint16(oversized[offset : offset+2])
		if tag != 256 && tag != 257 {
			continue
		}
		switch binary.LittleEndian.Uint16(oversized[offset+2 : offset+4]) {
		case 3:
			binary.LittleEndian.PutUint16(oversized[offset+8:offset+10], 30_000)
		case 4:
			binary.LittleEndian.PutUint32(oversized[offset+8:offset+12], 30_000)
		default:
			t.Fatalf("unexpected TIFF dimension field type")
		}
		changed++
	}
	if changed != 2 {
		t.Fatalf("changed %d TIFF dimensions, want 2", changed)
	}
	return valid, oversized
}

func TestLoadExistingImageReadsValidatedLocalUploadWithoutHTTP(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	temporary := t.TempDir()
	if err := os.Mkdir(filepath.Join(temporary, "uploads"), 0o750); err != nil {
		t.Fatalf("create uploads directory: %v", err)
	}
	var payload bytes.Buffer
	if err := png.Encode(&payload, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode image: %v", err)
	}
	uploadName := strings.Repeat("a", 64) + "-12345678-1234-4123-8123-123456789abc.png"
	if err := os.WriteFile(filepath.Join(temporary, "uploads", uploadName), payload.Bytes(), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := os.Chdir(temporary); err != nil {
		t.Fatalf("enter temporary directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })

	loaded, err := (&Handler{}).loadExistingImage(context.Background(), "/static/uploads/"+uploadName)
	if err != nil {
		t.Fatalf("loadExistingImage: %v", err)
	}
	if !bytes.Equal(loaded, payload.Bytes()) {
		t.Fatalf("loaded bytes = %d, want %d", len(loaded), payload.Len())
	}
	if _, err := (&Handler{}).loadExistingImage(context.Background(), "/static/uploads/fixture.png"); err == nil {
		t.Fatal("noncanonical local upload identity was readable")
	}
}
