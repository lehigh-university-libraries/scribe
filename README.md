# hOCRedit

A web-based hOCR editor with visual overlay editing and intelligent OCR processing optimized for handwritten text.

![Demo](./docs/assets/example.gif)

## Overview

hOCRedit uses [Laypa](https://github.com/knaw-huc/laypa) for document segmentation and baseline detection, combined with multi-provider LLM transcription to provide high-quality OCR results. Features a visual interface for correcting text directly on source images with real-time accuracy metrics.

### Architecture

- **Document Segmentation**: [Laypa](https://github.com/knaw-huc/laypa) generates PageXML with text regions and baselines
- **Format Conversion**: PageXML is transformed to hOCR format for editing
- **Transcription**: Multi-provider LLM support (OpenAI, Azure, Gemini, Ollama) for text recognition
- **Visual Editing**: Interactive overlay interface for corrections

## Features

- Visual text editing with bounding box overlays
- Laypa-powered document segmentation (regions and baselines)
- PageXML to hOCR conversion pipeline
- Multi-provider LLM transcription (OpenAI, Azure, Gemini, Ollama)
- Line-based editing and drawing mode for new regions
- Real-time accuracy metrics
- Islandora integration

## Quick Start

### Using Docker Compose (Recommended)

```bash
# Clone and setup
git clone https://github.com/lehigh-university-libraries/hOCRedit.git
cd hOCRedit

# Download a Laypa model (see LAYPA_SETUP.md)
mkdir -p laypa_models/default
# Place config.yml and model_final.pth in laypa_models/default/

# Start services
docker-compose up
```

Visit http://localhost:8888

### Standalone Docker (Not Recommended - Requires External Laypa)

```bash
docker run \
  -p 8888:8888 \
  -e LAYPA_API_URL=http://your-laypa-instance:5000 \
  -e OCR_PROVIDER=ollama \
  -e OLLAMA_HOST=http://host.docker.internal:11434 \
  ghcr.io/lehigh-university-libraries/hocredit:main
```

**Note**: Laypa is a required dependency. See [LAYPA_SETUP.md](./LAYPA_SETUP.md) for details.

## Configuration

- See [LAYPA_SETUP.md](./LAYPA_SETUP.md) for Laypa configuration
- See [PROVIDERS.md](./PROVIDERS.md) for LLM provider configuration
- See [sample.env](./sample.env) for environment variables

## Usage

1. Upload images, provide URLs, or Islandora node ID
2. Review OCR results with visual overlays
3. Click text regions to edit content
4. Use drawing mode to create new text regions
5. Monitor accuracy metrics in real-time
6. Export corrected hOCR or save to repositories

## Support

This project was sponsered thanks to a [Lyrasis Catalyst Fund](https://lyrasis.org/catalyst-fund/) grant awarded to Lehigh University.

## License

Apache 2.0
