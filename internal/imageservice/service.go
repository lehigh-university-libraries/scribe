package imageservice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/imagemagick"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
)

const maxImageRequestBytes int64 = 64 << 20

func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /v1/crop", handleCrop)
	mux.HandleFunc("POST /v1/stitch-horizontal", handleStitchHorizontal)
	mux.HandleFunc("POST /v1/normalize", handleNormalize)
	mux.Handle("/iiif/2/", http.HandlerFunc(handleIIIF))
	mux.Handle("/iiif/3/", http.HandlerFunc(handleIIIF))
	return mux
}

func handleCrop(w http.ResponseWriter, r *http.Request) {
	img, _, err := readMultipartImage(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	x, err := atoiField(r, "x")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	y, err := atoiField(r, "y")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	width, err := atoiField(r, "width")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	height, err := atoiField(r, "height")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rect := image.Rect(x, y, x+width, y+height).Intersect(img.Bounds())
	if rect.Dx() <= 0 || rect.Dy() <= 0 {
		http.Error(w, "invalid crop rectangle", http.StatusBadRequest)
		return
	}
	writeJPEG(w, cropImage(img, rect))
}

func handleStitchHorizontal(w http.ResponseWriter, r *http.Request) {
	img, _, err := readMultipartImage(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	padding, err := atoiOptionalField(r, "padding", 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rawBoxes := strings.TrimSpace(r.FormValue("boxes_json"))
	if rawBoxes == "" {
		http.Error(w, "boxes_json is required", http.StatusBadRequest)
		return
	}
	var boxes []Box
	if err := json.Unmarshal([]byte(rawBoxes), &boxes); err != nil {
		http.Error(w, fmt.Sprintf("parse boxes_json: %v", err), http.StatusBadRequest)
		return
	}
	out, err := stitchHorizontal(img, boxes, padding)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJPEG(w, out)
}

func handleNormalize(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImageRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
		return
	}
	normalized, err := normalizeWithMagick(body, r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(normalized)
}

func readMultipartImage(w http.ResponseWriter, r *http.Request) (image.Image, string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImageRequestBytes)
	if err := r.ParseMultipartForm(64 << 20); err != nil { // #nosec G120 -- request body is capped with http.MaxBytesReader immediately above.
		return nil, "", fmt.Errorf("parse multipart form: %w", err)
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		return nil, "", fmt.Errorf("read image form file: %w", err)
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, "", fmt.Errorf("decode image: %w", err)
	}
	return img, header.Filename, nil
}

func atoiField(r *http.Request, name string) (int, error) {
	return atoiOptionalField(r, name, 0)
}

func atoiOptionalField(r *http.Request, name string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(r.FormValue(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}

func cropImage(src image.Image, rect image.Rectangle) image.Image {
	if sub, ok := src.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return sub.SubImage(rect)
	}
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}

func stitchHorizontal(src image.Image, boxes []Box, padding int) (image.Image, error) {
	if len(boxes) == 0 {
		return nil, fmt.Errorf("no boxes to stitch")
	}
	totalWidth := 0
	maxHeight := 0
	crops := make([]image.Image, 0, len(boxes))
	for _, box := range boxes {
		rect := image.Rect(box.X-padding, box.Y-padding, box.X+box.Width+padding, box.Y+box.Height+padding).Intersect(src.Bounds())
		if rect.Dx() <= 0 || rect.Dy() <= 0 {
			continue
		}
		crop := cropImage(src, rect)
		crops = append(crops, crop)
		totalWidth += crop.Bounds().Dx()
		if crop.Bounds().Dy() > maxHeight {
			maxHeight = crop.Bounds().Dy()
		}
	}
	if len(crops) == 0 {
		return nil, fmt.Errorf("no valid boxes to stitch")
	}
	dst := image.NewRGBA(image.Rect(0, 0, totalWidth, maxHeight))
	offsetX := 0
	for _, crop := range crops {
		b := crop.Bounds()
		target := image.Rect(offsetX, 0, offsetX+b.Dx(), b.Dy())
		draw.Draw(dst, target, crop, b.Min, draw.Src)
		offsetX += b.Dx()
	}
	return dst, nil
}

func writeJPEG(w http.ResponseWriter, img image.Image) {
	w.Header().Set("Content-Type", "image/jpeg")
	_ = jpeg.Encode(w, img, &jpeg.Options{Quality: 95})
}

func normalizeWithMagick(imageData []byte, contentType string) ([]byte, error) {
	inputExt := extensionForContentType(contentType)
	inputFile, err := os.CreateTemp("", "image-normalize-*"+inputExt)
	if err != nil {
		return nil, fmt.Errorf("create temp input: %w", err)
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath)
	outputFile, err := os.CreateTemp("", "image-normalize-*.jpg")
	if err != nil {
		return nil, fmt.Errorf("create temp output: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath)

	if _, err := inputFile.Write(imageData); err != nil {
		_ = inputFile.Close()
		return nil, fmt.Errorf("write temp input: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("close temp input: %w", err)
	}

	cmd, err := imagemagick.ConvertCommand(inputPath, outputPath)
	if err != nil {
		return nil, err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("imagemagick normalize failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	normalized, err := safefile.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read normalized output: %w", err)
	}
	return normalized, nil
}

func extensionForContentType(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	switch contentType {
	case "image/jp2", "image/jpeg2000":
		return ".jp2"
	case "image/tiff", "image/tif":
		return ".tif"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	default:
		return ".img"
	}
}

func EncodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
