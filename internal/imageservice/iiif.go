package imageservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/uploadblob"
)

const iiifUploadsEnv = "IMAGE_SERVICE_UPLOADS_DIR"

type iiifRequest struct {
	Version      string
	Identifier   string
	Region       string
	Size         string
	Rotation     string
	Quality      string
	Format       string
	InfoJSONOnly bool
}

func handleIIIF(w http.ResponseWriter, r *http.Request) {
	req, err := parseIIIFRequest(r.URL.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	imagePath, cleanup, err := iiifUploadPathFromIdentifier(r.Context(), req.Identifier)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer cleanup()
	if _, err := os.Stat(imagePath); err != nil {
		http.NotFound(w, r)
		return
	}

	if req.InfoJSONOnly {
		serveIIIFInfoJSON(w, r, req, imagePath)
		return
	}
	serveIIIFImageBytes(w, req, imagePath)
}

func parseIIIFRequest(path string) (iiifRequest, error) {
	trimmed := strings.TrimSpace(path)
	for _, version := range []string{"2", "3"} {
		prefix := "/iiif/" + version + "/"
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		rest := strings.TrimPrefix(trimmed, prefix)
		if rest == "" {
			break
		}
		parts := strings.Split(rest, "/")
		if len(parts) == 2 && parts[1] == "info.json" {
			return iiifRequest{
				Version:      version,
				Identifier:   parts[0],
				InfoJSONOnly: true,
			}, nil
		}
		if len(parts) != 5 {
			return iiifRequest{}, fmt.Errorf("invalid IIIF image path %q", path)
		}
		quality, format, found := strings.Cut(parts[4], ".")
		if !found || strings.TrimSpace(quality) == "" || strings.TrimSpace(format) == "" {
			return iiifRequest{}, fmt.Errorf("invalid IIIF quality/format segment %q", parts[4])
		}
		return iiifRequest{
			Version:    version,
			Identifier: parts[0],
			Region:     parts[1],
			Size:       parts[2],
			Rotation:   parts[3],
			Quality:    quality,
			Format:     strings.ToLower(strings.TrimSpace(format)),
		}, nil
	}
	return iiifRequest{}, fmt.Errorf("path %q is not a IIIF image request", path)
}

func iiifUploadPathFromIdentifier(ctx context.Context, identifier string) (string, func(), error) {
	decoded, err := url.PathUnescape(strings.TrimSpace(identifier))
	if err != nil {
		return "", func() {}, fmt.Errorf("decode IIIF identifier: %w", err)
	}
	if decoded == "" {
		return "", func() {}, fmt.Errorf("empty IIIF identifier")
	}
	if decoded == "." || decoded == ".." || strings.Contains(decoded, "..") || strings.Contains(decoded, "/") || strings.Contains(decoded, "\\") {
		return "", func() {}, fmt.Errorf("invalid IIIF identifier %q", identifier)
	}
	if uploadblob.Enabled() {
		data, _, err := uploadblob.Read(ctx, decoded)
		if err != nil {
			return "", func() {}, err
		}
		f, err := os.CreateTemp("", "scribe-iiif-*"+filepath.Ext(decoded))
		if err != nil {
			return "", func() {}, fmt.Errorf("create iiif temp file: %w", err)
		}
		cleanup := func() { _ = os.Remove(f.Name()) }
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("write iiif temp file: %w", err)
		}
		if err := f.Close(); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("close iiif temp file: %w", err)
		}
		return f.Name(), cleanup, nil
	}
	root := strings.TrimSpace(os.Getenv(iiifUploadsEnv))
	if root == "" {
		root = "uploads"
	}
	return filepath.Join(root, decoded), func() {}, nil
}

func serveIIIFInfoJSON(w http.ResponseWriter, r *http.Request, req iiifRequest, imagePath string) {
	cfg, err := decodeIIIFImageConfig(imagePath)
	if err != nil {
		slog.Warn("iiif info.json decode failed", "path", imagePath, "error", err)
		http.Error(w, "iiif image unavailable", http.StatusInternalServerError)
		return
	}

	serviceID := requestOrigin(r) + "/iiif/" + req.Version + "/" + req.Identifier
	if req.Version == "3" {
		writeJSON(w, http.StatusOK, map[string]any{
			"@context":       "http://iiif.io/api/image/3/context.json",
			"id":             serviceID,
			"type":           "ImageService3",
			"protocol":       "http://iiif.io/api/image",
			"profile":        "level2",
			"width":          cfg.Width,
			"height":         cfg.Height,
			"extraFormats":   []string{"jpg", "png"},
			"extraQualities": []string{"default", "color", "gray"},
			"extraFeatures": []string{
				"regionByPx",
				"sizeByWh",
				"sizeByPct",
				"rotationBy90s",
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"@context": "http://iiif.io/api/image/2/context.json",
		"@id":      serviceID,
		"protocol": "http://iiif.io/api/image",
		"profile": []any{
			"http://iiif.io/api/image/2/level2.json",
			map[string]any{
				"formats":   []string{"jpg", "png"},
				"qualities": []string{"default", "color", "gray"},
				"supports": []string{
					"regionByPx",
					"sizeByW",
					"sizeByH",
					"sizeByPct",
					"sizeByWh",
					"sizeByConfinedWh",
					"rotationBy90s",
				},
			},
		},
		"width":  cfg.Width,
		"height": cfg.Height,
	})
}

func serveIIIFImageBytes(w http.ResponseWriter, req iiifRequest, imagePath string) {
	src, err := decodeIIIFImage(imagePath)
	if err != nil {
		slog.Warn("iiif image decode failed", "path", imagePath, "error", err)
		http.Error(w, "iiif image unavailable", http.StatusInternalServerError)
		return
	}

	region, err := parseIIIFRegionSpec(req.Region, src.Bounds())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	out := cropIIIFImage(src, region)
	sizeW, sizeH, err := parseIIIFSizeSpec(req.Size, region.Dx(), region.Dy())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if sizeW != out.Bounds().Dx() || sizeH != out.Bounds().Dy() {
		out = resizeIIIFImageNearest(out, sizeW, sizeH)
	}

	out, err = applyIIIFRotation(out, req.Rotation)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	out, err = applyIIIFQuality(out, req.Quality)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch req.Format {
	case "jpg", "jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
		if err := jpeg.Encode(w, out, &jpeg.Options{Quality: 90}); err != nil {
			slog.Warn("iiif jpeg encode failed", "path", imagePath, "error", err)
		}
	case "png":
		w.Header().Set("Content-Type", "image/png")
		if err := png.Encode(w, out); err != nil {
			slog.Warn("iiif png encode failed", "path", imagePath, "error", err)
		}
	default:
		http.Error(w, fmt.Sprintf("unsupported IIIF format %q", req.Format), http.StatusBadRequest)
	}
}

func decodeIIIFImageConfig(imagePath string) (image.Config, error) {
	f, err := safefile.Open(imagePath)
	if err != nil {
		return image.Config{}, err
	}
	cfg, _, decodeErr := image.DecodeConfig(f)
	_ = f.Close()
	if decodeErr == nil {
		return cfg, nil
	}

	normalized, err := normalizeIIIFSource(imagePath)
	if err != nil {
		return image.Config{}, fmt.Errorf("decode config: %w (normalize fallback failed: %v)", decodeErr, err)
	}
	cfg, _, err = image.DecodeConfig(bytes.NewReader(normalized))
	if err != nil {
		return image.Config{}, fmt.Errorf("decode normalized config: %w", err)
	}
	return cfg, nil
}

func decodeIIIFImage(imagePath string) (image.Image, error) {
	f, err := safefile.Open(imagePath)
	if err != nil {
		return nil, err
	}
	img, _, decodeErr := image.Decode(f)
	_ = f.Close()
	if decodeErr == nil {
		return img, nil
	}

	normalized, err := normalizeIIIFSource(imagePath)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w (normalize fallback failed: %v)", decodeErr, err)
	}
	img, _, err = image.Decode(bytes.NewReader(normalized))
	if err != nil {
		return nil, fmt.Errorf("decode normalized image: %w", err)
	}
	return img, nil
}

func normalizeIIIFSource(imagePath string) ([]byte, error) {
	data, err := safefile.ReadFile(imagePath)
	if err != nil {
		return nil, err
	}
	return normalizeWithMagick(data, detectIIIFContentType(imagePath, data))
}

func detectIIIFContentType(imagePath string, data []byte) string {
	contentType := http.DetectContentType(data)
	if contentType != "application/octet-stream" {
		return contentType
	}
	switch strings.ToLower(filepath.Ext(imagePath)) {
	case ".jp2", ".j2k", ".jpx":
		return "image/jp2"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

func parseIIIFRegionSpec(spec string, bounds image.Rectangle) (image.Rectangle, error) {
	if spec == "" || spec == "full" {
		return bounds, nil
	}
	if strings.HasPrefix(spec, "pct:") {
		values, err := parseIIIFNumericList(strings.TrimPrefix(spec, "pct:"), 4)
		if err != nil {
			return image.Rectangle{}, err
		}
		width := float64(bounds.Dx())
		height := float64(bounds.Dy())
		x := bounds.Min.X + int(math.Round(width*values[0]/100))
		y := bounds.Min.Y + int(math.Round(height*values[1]/100))
		w := int(math.Round(width * values[2] / 100))
		h := int(math.Round(height * values[3] / 100))
		return clampIIIFRect(bounds, image.Rect(x, y, x+w, y+h))
	}

	values, err := parseIIIFIntegerList(spec, 4)
	if err != nil {
		return image.Rectangle{}, err
	}
	return clampIIIFRect(bounds, image.Rect(values[0], values[1], values[0]+values[2], values[1]+values[3]))
}

func parseIIIFSizeSpec(spec string, sourceW, sourceH int) (int, int, error) {
	if sourceW <= 0 || sourceH <= 0 {
		return 0, 0, fmt.Errorf("invalid source size")
	}
	if spec == "" || spec == "full" || spec == "max" {
		return sourceW, sourceH, nil
	}
	if strings.HasPrefix(spec, "pct:") {
		pct, err := strconv.ParseFloat(strings.TrimPrefix(spec, "pct:"), 64)
		if err != nil || pct <= 0 {
			return 0, 0, fmt.Errorf("invalid IIIF size %q", spec)
		}
		w, h := scaledIIIFDimensions(sourceW, sourceH, pct/100)
		return w, h, nil
	}
	if strings.HasPrefix(spec, "!") {
		values, err := parseIIIFIntegerList(strings.TrimPrefix(spec, "!"), 2)
		if err != nil {
			return 0, 0, err
		}
		maxW := values[0]
		maxH := values[1]
		if maxW <= 0 || maxH <= 0 {
			return 0, 0, fmt.Errorf("invalid IIIF size %q", spec)
		}
		scale := math.Min(float64(maxW)/float64(sourceW), float64(maxH)/float64(sourceH))
		w, h := scaledIIIFDimensions(sourceW, sourceH, scale)
		return w, h, nil
	}
	if strings.HasSuffix(spec, ",") {
		width, err := strconv.Atoi(strings.TrimSuffix(spec, ","))
		if err != nil || width <= 0 {
			return 0, 0, fmt.Errorf("invalid IIIF size %q", spec)
		}
		scale := float64(width) / float64(sourceW)
		w, h := scaledIIIFDimensions(sourceW, sourceH, scale)
		return w, h, nil
	}
	if strings.HasPrefix(spec, ",") {
		height, err := strconv.Atoi(strings.TrimPrefix(spec, ","))
		if err != nil || height <= 0 {
			return 0, 0, fmt.Errorf("invalid IIIF size %q", spec)
		}
		scale := float64(height) / float64(sourceH)
		w, h := scaledIIIFDimensions(sourceW, sourceH, scale)
		return w, h, nil
	}

	values, err := parseIIIFIntegerList(spec, 2)
	if err != nil {
		return 0, 0, err
	}
	if values[0] <= 0 || values[1] <= 0 {
		return 0, 0, fmt.Errorf("invalid IIIF size %q", spec)
	}
	return values[0], values[1], nil
}

func scaledIIIFDimensions(sourceW, sourceH int, scale float64) (int, int) {
	width := max(1, int(math.Round(float64(sourceW)*scale)))
	height := max(1, int(math.Round(float64(sourceH)*scale)))
	return width, height
}

func applyIIIFRotation(src image.Image, spec string) (image.Image, error) {
	if spec == "" || spec == "0" || spec == "0.0" {
		return src, nil
	}
	if strings.HasPrefix(spec, "!") {
		return nil, fmt.Errorf("mirrored IIIF rotations are not supported")
	}
	rotation, err := strconv.ParseFloat(spec, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid IIIF rotation %q", spec)
	}
	rotation = math.Mod(rotation, 360)
	if rotation < 0 {
		rotation += 360
	}

	const epsilon = 0.001
	switch {
	case math.Abs(rotation-0) < epsilon || math.Abs(rotation-360) < epsilon:
		return src, nil
	case math.Abs(rotation-90) < epsilon:
		return rotateIIIF90(src), nil
	case math.Abs(rotation-180) < epsilon:
		return rotateIIIF180(src), nil
	case math.Abs(rotation-270) < epsilon:
		return rotateIIIF270(src), nil
	default:
		return nil, fmt.Errorf("unsupported IIIF rotation %q", spec)
	}
}

func applyIIIFQuality(src image.Image, quality string) (image.Image, error) {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "", "default", "color":
		return src, nil
	case "gray", "grey":
		bounds := src.Bounds()
		dst := image.NewGray(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				dst.Set(x-bounds.Min.X, y-bounds.Min.Y, color.GrayModel.Convert(src.At(x, y)))
			}
		}
		return dst, nil
	default:
		return nil, fmt.Errorf("unsupported IIIF quality %q", quality)
	}
}

func cropIIIFImage(src image.Image, rect image.Rectangle) image.Image {
	if sub, ok := src.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return sub.SubImage(rect)
	}
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}

func resizeIIIFImageNearest(src image.Image, width, height int) image.Image {
	srcBounds := src.Bounds()
	if width == srcBounds.Dx() && height == srcBounds.Dy() {
		return src
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		srcY := srcBounds.Min.Y + (y*srcBounds.Dy())/height
		for x := 0; x < width; x++ {
			srcX := srcBounds.Min.X + (x*srcBounds.Dx())/width
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}

func rotateIIIF90(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dst.Set(bounds.Max.Y-y-1, x-bounds.Min.X, src.At(x, y))
		}
	}
	return dst
}

func rotateIIIF180(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dst.Set(bounds.Max.X-x-1, bounds.Max.Y-y-1, src.At(x, y))
		}
	}
	return dst
}

func rotateIIIF270(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dst.Set(y-bounds.Min.Y, bounds.Max.X-x-1, src.At(x, y))
		}
	}
	return dst
}

func clampIIIFRect(bounds, rect image.Rectangle) (image.Rectangle, error) {
	rect = rect.Intersect(bounds)
	if rect.Dx() <= 0 || rect.Dy() <= 0 {
		return image.Rectangle{}, fmt.Errorf("invalid IIIF region")
	}
	return rect, nil
}

func parseIIIFIntegerList(raw string, expected int) ([]int, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != expected {
		return nil, fmt.Errorf("invalid IIIF segment %q", raw)
	}
	values := make([]int, 0, expected)
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("invalid IIIF segment %q", raw)
		}
		values = append(values, value)
	}
	return values, nil
}

func parseIIIFNumericList(raw string, expected int) ([]float64, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != expected {
		return nil, fmt.Errorf("invalid IIIF segment %q", raw)
	}
	values := make([]float64, 0, expected)
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid IIIF segment %q", raw)
		}
		values = append(values, value)
	}
	return values, nil
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.Split(forwarded, ",")[0]
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}
