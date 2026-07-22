package handlers

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	maxRemoteImageBytes   int64 = uploadlimits.MaxImageBytes
	maxUploadedImageBytes       = maxRemoteImageBytes
	maxImageDimension           = uploadlimits.MaxImageDimension
	maxImagePixels              = uploadlimits.MaxImagePixels
)

type invalidImageError struct {
	message string
}

func (e *invalidImageError) Error() string {
	return e.message
}

func invalidImageErrorf(format string, args ...any) error {
	return &invalidImageError{message: fmt.Sprintf(format, args...)}
}

// IsInvalidImageError reports whether an image-processing failure is safe to
// return as client input feedback. All other pipeline errors may contain local
// paths, provider diagnostics, or infrastructure details and must be logged
// server-side instead.
func IsInvalidImageError(err error) bool {
	var invalid *invalidImageError
	return errors.As(err, &invalid)
}

func (h *Handler) downloadImageFromURL(ctx context.Context, imageURL string) ([]byte, error) {
	resp, err := safehttp.Get(ctx, imageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download image: HTTP %d", resp.StatusCode)
	}

	imageData, err := safehttp.ReadAllLimit(resp.Body, maxRemoteImageBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read image data: %w", err)
	}

	// The upstream Content-Type is attacker-controlled and deliberately ignored.
	return imageData, nil
}

func validateUploadedImageData(fileData []byte) error {
	_, err := uploadedImageConfig(fileData)
	return err
}

func uploadedImageConfig(fileData []byte) (image.Config, error) {
	if len(fileData) == 0 {
		return image.Config{}, invalidImageErrorf("image data is required")
	}
	if int64(len(fileData)) > maxUploadedImageBytes {
		return image.Config{}, invalidImageErrorf("image data exceeds 100 MiB limit")
	}
	mediaType, _, err := canonicalImageMediaType(fileData)
	if err != nil {
		return image.Config{}, err
	}
	var cfg image.Config
	if mediaType == "image/jp2" {
		cfg.Width, cfg.Height, err = decodeJPEG2000Dimensions(fileData)
	} else {
		cfg, _, err = image.DecodeConfig(bytes.NewReader(fileData))
	}
	if err != nil {
		return image.Config{}, invalidImageErrorf("invalid %s image data", mediaType)
	}
	if err := validateImageDimensions(cfg.Width, cfg.Height); err != nil {
		return image.Config{}, err
	}
	return cfg, nil
}

// ValidateUploadedImageData applies the public upload size, type, dimension,
// and decoded-pixel limits without invoking storage or a model provider.
func ValidateUploadedImageData(fileData []byte) error {
	return validateUploadedImageData(fileData)
}

// UploadedImageDimensions validates an upload and returns the decoded Canvas
// dimensions used by canonical IIIF geometry checks. The configured limits
// keep both values safely within uint32.
func UploadedImageDimensions(fileData []byte) (uint32, uint32, error) {
	cfg, err := uploadedImageConfig(fileData)
	if err != nil {
		return 0, 0, err
	}
	return uint32(cfg.Width), uint32(cfg.Height), nil // #nosec G115 -- validated against maxImageDimension.
}

// UploadedImageMediaType returns the canonical media type recognized by the
// upload validator. Raw-source delivery and model requests share this one
// signature implementation so TIFF and JPEG 2000 are not admitted at upload
// time and then rejected when Triplet reads the immutable source.
func UploadedImageMediaType(fileData []byte) (string, error) {
	mediaType, _, err := canonicalImageMediaType(fileData)
	return mediaType, err
}

func validateImageDimensions(width, height int) error {
	if err := uploadlimits.ValidateImageDimensions(width, height); err != nil {
		return invalidImageErrorf("%s", err)
	}
	return nil
}

func canonicalImageMediaType(data []byte) (string, string, error) {
	detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	switch detected {
	case "image/jpeg":
		return "image/jpeg", ".jpg", nil
	case "image/png":
		return "image/png", ".png", nil
	case "image/gif":
		return "image/gif", ".gif", nil
	case "image/webp":
		return "image/webp", ".webp", nil
	}
	// net/http intentionally classifies TIFF and JPEG 2000 signatures as
	// application/octet-stream, so recognize those formats explicitly.
	if len(data) >= 4 && ((data[0] == 'I' && data[1] == 'I' && data[2] == 42 && data[3] == 0) ||
		(data[0] == 'M' && data[1] == 'M' && data[2] == 0 && data[3] == 42)) {
		return "image/tiff", ".tiff", nil
	}
	if len(data) >= 4 && data[0] == 0xff && data[1] == 0x4f && data[2] == 0xff && data[3] == 0x51 {
		return "image/jp2", ".jp2", nil
	}
	if len(data) >= 12 && bytes.Equal(data[:12], []byte{0, 0, 0, 12, 'j', 'P', ' ', ' ', 13, 10, 0x87, 10}) {
		return "image/jp2", ".jp2", nil
	}
	return "", "", invalidImageErrorf("unsupported image content type %q", detected)
}

func decodeJPEG2000Dimensions(data []byte) (int, int, error) {
	if len(data) >= 2 && data[0] == 0xff && data[1] == 0x4f {
		return decodeJPEG2000CodestreamDimensions(data)
	}
	if len(data) < 12 || !bytes.Equal(data[:12], []byte{0, 0, 0, 12, 'j', 'P', ' ', ' ', 13, 10, 0x87, 10}) {
		return 0, 0, fmt.Errorf("missing JPEG 2000 signature")
	}
	width, height, found, err := findJP2ImageHeader(data[12:], 0)
	if err != nil {
		return 0, 0, err
	}
	if !found {
		return 0, 0, fmt.Errorf("missing JPEG 2000 image header")
	}
	return width, height, nil
}

func decodeJPEG2000CodestreamDimensions(data []byte) (int, int, error) {
	// SIZ is the first marker segment after the SOC marker. Its reference-grid
	// extent minus origin is the decoded image size.
	if len(data) < 24 || data[2] != 0xff || data[3] != 0x51 {
		return 0, 0, fmt.Errorf("missing JPEG 2000 SIZ marker")
	}
	segmentLength := int(binary.BigEndian.Uint16(data[4:6]))
	if segmentLength < 38 || 4+segmentLength > len(data) {
		return 0, 0, fmt.Errorf("invalid JPEG 2000 SIZ marker")
	}
	xSize := binary.BigEndian.Uint32(data[8:12])
	ySize := binary.BigEndian.Uint32(data[12:16])
	xOrigin := binary.BigEndian.Uint32(data[16:20])
	yOrigin := binary.BigEndian.Uint32(data[20:24])
	return boundedDimensions(xSize, ySize, xOrigin, yOrigin)
}

func findJP2ImageHeader(data []byte, depth int) (int, int, bool, error) {
	if depth > 4 {
		return 0, 0, false, fmt.Errorf("jpeg 2000 box nesting is too deep")
	}
	for offset := 0; offset < len(data); {
		if len(data)-offset < 8 {
			return 0, 0, false, fmt.Errorf("truncated JPEG 2000 box")
		}
		boxLength := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		boxType := string(data[offset+4 : offset+8])
		headerLength := uint64(8)
		switch boxLength {
		case 1:
			if len(data)-offset < 16 {
				return 0, 0, false, fmt.Errorf("truncated extended JPEG 2000 box")
			}
			boxLength = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			headerLength = 16
		case 0:
			boxLength = uint64(len(data) - offset)
		}
		if boxLength < headerLength || boxLength > uint64(len(data)-offset) {
			return 0, 0, false, fmt.Errorf("invalid JPEG 2000 box length")
		}
		// Both lengths are bounded by len(data)-offset immediately above, so
		// they fit the platform int used by slice indexes.
		payload := data[offset+int(headerLength) : offset+int(boxLength)] // #nosec G115 -- bounded by the current slice length.
		switch boxType {
		case "ihdr":
			if len(payload) < 14 {
				return 0, 0, false, fmt.Errorf("truncated JPEG 2000 image header")
			}
			height := binary.BigEndian.Uint32(payload[0:4])
			width := binary.BigEndian.Uint32(payload[4:8])
			boundedWidth, boundedHeight, boundsErr := boundedDimensions(width, height, 0, 0)
			return boundedWidth, boundedHeight, true, boundsErr
		case "jp2h":
			width, height, found, findErr := findJP2ImageHeader(payload, depth+1)
			if findErr != nil || found {
				return width, height, found, findErr
			}
		}
		offset += int(boxLength) // #nosec G115 -- bounded by len(data)-offset above.
	}
	return 0, 0, false, nil
}

func boundedDimensions(xSize, ySize, xOrigin, yOrigin uint32) (int, int, error) {
	if xSize <= xOrigin || ySize <= yOrigin {
		return 0, 0, fmt.Errorf("invalid JPEG 2000 image extent")
	}
	width, height := uint64(xSize-xOrigin), uint64(ySize-yOrigin)
	maxIntValue := uint64(^uint(0) >> 1)
	if width > maxIntValue || height > maxIntValue {
		return 0, 0, fmt.Errorf("jpeg 2000 image extent overflows platform integers")
	}
	return int(width), int(height), nil
}
