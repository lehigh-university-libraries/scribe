# hOCRedit

A web-based hOCR editor with visual overlay editing and intelligent OCR processing optimized for handwritten text.

![Demo](./docs/assets/example.gif)

## Overview

hOCRedit uses Tesseract for word bounding box detection combined with LLM transcription (Ollama or OpenAI) to provide high-quality OCR results. Features a visual interface for correcting text directly on source images with real-time accuracy metrics.

## Features

- Visual text editing with bounding box overlays
- Tesseract-based word detection with precise bounding boxes
- Hybrid OCR: Tesseract word boundaries + LLM word-by-word transcription
- Support for multiple LLM providers (Ollama, OpenAI)
- Line-based editing and drawing mode for new regions
- Islandora integration

## Quick Start

### Using Ollama (Default)

```bash
docker run \
  -p 8888:8888 \
  -e LLM_PROVIDER=ollama \
  -e OLLAMA_URL=http://host.docker.internal:11434 \
  -e OLLAMA_MODEL=mistral-small3.2:24b \
  ghcr.io/lehigh-university-libraries/hocredit:main
```

### Using OpenAI

```bash
docker run \
  -p 8888:8888 \
  -e LLM_PROVIDER=openai \
  -e OPENAI_API_KEY=your-key \
  -e OPENAI_MODEL=gpt-4o \
  ghcr.io/lehigh-university-libraries/hocredit:main
```

## Configuration

hOCRedit supports two LLM providers for transcription:

- **Ollama** (default): Local inference, no API costs, requires local Ollama installation
- **OpenAI**: Cloud-based, requires API key and credits

See [sample.env](./sample.env) for all configuration options

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
