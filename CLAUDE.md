# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

hOCRedit is a web-based hOCR editor with visual overlay editing and intelligent OCR processing optimized for handwritten text. It uses a custom word bounding box algorithm combined with LLM transcription (OpenAI, Azure, Gemini, or Ollama) to provide high-quality OCR results.

## Architecture

### Backend (Go)
- **main.go**: HTTP server setup listening on port 8888
- **internal/handlers/**: HTTP request handlers
  - `common.go`: Handler struct and shared utilities
  - `upload.go`: Image upload and URL/Drupal node processing
  - `image_processing.go`: Image processing pipeline coordination
  - `hocr.go`: hOCR parsing and updates
  - `sessions.go`: Session management endpoints
  - `drupal.go`: Islandora/Drupal integration
  - `static.go`: Static file serving
- **internal/hocr/service.go**: Core OCR pipeline
  - Custom word detection using ImageMagick preprocessing and connected component analysis
  - Line grouping algorithm to organize words into text lines
  - LLM transcription integration via provider service
  - Creates stitched image with hOCR markup for LLM context
- **internal/providers/service.go**: Multi-provider LLM abstraction
  - Wraps github.com/lehigh-university-libraries/htr package
  - Supports: OpenAI, Azure OpenAI, Google Gemini, Ollama
  - Default provider: Ollama (can be changed via OCR_PROVIDER env var)
- **internal/storage/**: In-memory session storage
- **internal/models/**: Data structures for OCR responses and sessions
- **internal/metrics/**: Accuracy calculation (edit distance-based)
- **internal/utils/**: Error handling utilities

### Frontend (Vanilla JS)
- **static/index.html**: Single-page application UI
- **static/script.js**: Client-side logic for:
  - Image overlay rendering with bounding boxes
  - Text editing with word/line navigation (Tab/Shift+Tab)
  - Drawing mode for creating new text regions (d key)
  - Real-time accuracy metrics
  - Session management

### OCR Pipeline Flow
1. Image upload → ImageMagick preprocessing (grayscale, contrast, threshold)
2. Connected component analysis to detect word bounding boxes
3. Group words into lines based on Y-coordinate clustering
4. Create stitched image with hOCR markup overlays
5. Send to LLM provider (with prompt in internal/providers/service.go:69-82)
6. Parse LLM response and return hOCR XML

## Development Commands

### Build and Run
```bash
# Build Docker image
docker build -t hocredit .

# Run container
docker run -p 8888:8888 \
  -e OCR_PROVIDER=ollama \
  -e OLLAMA_HOST=http://host.docker.internal:11434 \
  -e OLLAMA_MODEL=mistral-small3.2:24b \
  hocredit

# Build and run Go binary directly
go build -o hOCRedit
./hOCRedit
```

### Testing
```bash
# Run tests
go test ./internal/metrics/...

# Test with curl
curl http://localhost:8888/healthcheck
```

## Configuration

See PROVIDERS.md for multi-provider configuration details. Key environment variables:
- `OCR_PROVIDER`: openai|azure|gemini|ollama (default: ollama)
- `OPENAI_API_KEY`, `OPENAI_MODEL`: OpenAI configuration
- `AZURE_API_KEY`, `AZURE_ENDPOINT`, `AZURE_DEPLOYMENT`, `AZURE_MODEL`: Azure configuration
- `GEMINI_API_KEY`, `GEMINI_MODEL`: Gemini configuration
- `OLLAMA_HOST`, `OLLAMA_MODEL`: Ollama configuration
- `DRUPAL_HOCR_URL`: Template for Drupal/Islandora integration

## Key Implementation Details

### Custom Word Detection Algorithm
The word detection (internal/hocr/service.go:140-295) uses:
- Flood fill to find connected components
- Size filtering (8x10 min, half-image-width max)
- Horizontal merging of nearby components (gap ≤ 1/3 character height)
- Vertical overlap detection for line grouping

### Provider Integration
LLM providers are accessed through the HTR tool's provider interface. To add a new provider, implement `providers.Provider` and register in `internal/providers/service.go:22-29`.

### Frontend Navigation
- Tab: Next line
- Shift+Tab: Previous line
- Enter: Save word edit
- Delete: Remove selected line
- Escape: Clear selection or exit drawing mode
- d: Toggle drawing mode

## Dependencies
- ImageMagick (magick command) for image preprocessing
- Go 1.24.7+
- github.com/lehigh-university-libraries/htr for LLM providers
