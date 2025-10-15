# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

hOCRedit is a web-based hOCR editor with visual overlay editing and intelligent OCR processing optimized for handwritten text. It uses [Laypa](https://github.com/knaw-huc/laypa) for document segmentation and baseline detection, combined with multi-provider LLM transcription (OpenAI, Azure, Gemini, or Ollama) to provide high-quality OCR results.

### Processing Pipeline

1. **Document Segmentation**: Laypa generates PageXML with text regions and baselines
2. **Format Conversion**: PageXML is converted to hOCR format (internal/pagexml/converter.go)
3. **Transcription**: LLM providers transcribe text regions
4. **Visual Editing**: Interactive overlay interface for corrections

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
- **internal/laypa/service.go**: Laypa API integration
  - Communicates with Laypa service via REST API
  - Sends images for segmentation
  - Receives PageXML with text regions and baselines
- **internal/pagexml/converter.go**: PageXML to hOCR conversion
  - Parses PageXML output from Laypa
  - Converts TextRegions and TextLines to hOCR format
  - Preserves bounding boxes for visual overlay
- **internal/hocr/service.go**: Core OCR pipeline orchestration
  - Uses Laypa for document segmentation (required dependency)
  - Integrates LLM transcription via provider service
  - Coordinates PageXML → hOCR conversion
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
1. Image upload → Send to Laypa API for segmentation
2. Laypa processes image and returns PageXML with:
   - Text regions (bounding boxes)
   - Baselines (text line coordinates)
   - Region hierarchies
3. PageXML converted to internal hOCR format (internal/pagexml/converter.go)
4. Text regions sent to LLM provider for transcription (internal/providers/service.go)
5. LLM response parsed and merged with bounding box data
6. Final hOCR XML returned with coordinates + transcribed text

## Development Commands

### Build and Run
```bash
# Using Docker Compose (Recommended - includes Laypa)
docker-compose up

# Build Docker image only (requires external Laypa)
docker build -t hocredit .

# Run container (requires Laypa API)
docker run -p 8888:8888 \
  -e LAYPA_API_URL=http://laypa:5000 \
  -e OCR_PROVIDER=ollama \
  -e OLLAMA_HOST=http://host.docker.internal:11434 \
  -e OLLAMA_MODEL=mistral-small3.2:24b \
  hocredit

# Build and run Go binary directly (requires Laypa API running)
export LAYPA_API_URL=http://localhost:5000
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

See [LAYPA_SETUP.md](./LAYPA_SETUP.md) for Laypa configuration and [PROVIDERS.md](./PROVIDERS.md) for LLM provider configuration.

Key environment variables:
- **Laypa Configuration**:
  - `LAYPA_API_URL`: URL of Laypa API service (default: http://localhost:5000)
  - `LAYPA_MODEL_NAME`: Model folder name in Laypa (default: default)
- **LLM Provider Configuration**:
  - `OCR_PROVIDER`: openai|azure|gemini|ollama (default: ollama)
  - `OPENAI_API_KEY`, `OPENAI_MODEL`: OpenAI configuration
  - `AZURE_API_KEY`, `AZURE_ENDPOINT`, `AZURE_DEPLOYMENT`, `AZURE_MODEL`: Azure configuration
  - `GEMINI_API_KEY`, `GEMINI_MODEL`: Gemini configuration
  - `OLLAMA_HOST`, `OLLAMA_MODEL`: Ollama configuration
- **Integration**:
  - `DRUPAL_HOCR_URL`: Template for Drupal/Islandora integration

## Key Implementation Details

### Laypa Integration
Document segmentation is handled by [Laypa](https://github.com/knaw-huc/laypa) (internal/laypa/service.go):
- Communicates with Laypa via REST API
- Sends images to `/predict` endpoint
- Receives PageXML with text regions and baselines
- Laypa is a **required dependency** - app will not start without it

### PageXML to hOCR Conversion
The converter (internal/pagexml/converter.go) transforms Laypa's output:
- Parses PageXML structure (TextRegions, TextLines, Baselines)
- Extracts bounding boxes from coordinate strings
- Converts to internal OCRResponse format
- Preserves spatial information for visual overlay

### Provider Integration
LLM providers are accessed through the HTR tool's provider interface. To add a new provider, implement `providers.Provider` and register in `internal/providers/service.go`.

### Frontend Navigation
- Tab: Next line
- Shift+Tab: Previous line
- Enter: Save word edit
- Delete: Remove selected line
- Escape: Clear selection or exit drawing mode
- d: Toggle drawing mode

## Dependencies
- **Laypa**: Document segmentation service (required) - https://github.com/knaw-huc/laypa
- **ImageMagick**: Image utilities (magick command)
- **Go 1.24.7+**: Backend language
- **github.com/lehigh-university-libraries/htr**: LLM provider abstraction
