# Multi-Provider Support

hOCRedit supports multiple LLM providers for text transcription. Document segmentation is handled by [Laypa](https://github.com/knaw-huc/laypa), which generates PageXML with text regions and baselines. The LLM providers then transcribe the detected text regions using the provider implementations from the HTR tool.

## Pipeline Overview

1. **Document Segmentation**: [Laypa](https://github.com/knaw-huc/laypa) detects text regions and baselines (generates PageXML)
2. **Format Conversion**: PageXML converted to hOCR format
3. **Transcription**: LLM provider transcribes detected text regions (this document covers this step)
4. **Output**: Combined hOCR with bounding boxes + transcribed text

## Available Providers

- **openai** - OpenAI GPT models (default)
- **azure** - Azure OpenAI Service
- **gemini** - Google Gemini models
- **ollama** - Local Ollama models

## Configuration

Configure providers using environment variables:

### Provider Selection
```bash
# Choose which provider to use (default: openai)
export OCR_PROVIDER=openai
# or
export OCR_PROVIDER=azure
# or
export OCR_PROVIDER=gemini
# or
export OCR_PROVIDER=ollama
```

### Provider-Specific Configuration

#### OpenAI
```bash
export OPENAI_API_KEY=your_api_key_here
export OPENAI_MODEL=gpt-4o  # default: gpt-4o
```

#### Azure OpenAI
```bash
export AZURE_API_KEY=your_api_key_here
export AZURE_ENDPOINT=https://your-resource.openai.azure.com/
export AZURE_DEPLOYMENT=your_deployment_name
export AZURE_MODEL=gpt-4o  # default: gpt-4o
```

#### Google Gemini
```bash
export GEMINI_API_KEY=your_api_key_here
export GEMINI_MODEL=gemini-1.5-flash  # default: gemini-1.5-flash
```

#### Ollama (Local)
```bash
export OLLAMA_HOST=http://localhost:11434  # default
export OLLAMA_MODEL=llama2-vision  # default: llama2-vision
```

## Usage

Once configured, hOCRedit will automatically use the specified provider for transcription tasks. The application maintains the same API and functionality - only the underlying provider changes.

## Backward Compatibility

For backward compatibility, hOCRedit defaults to OpenAI if no `OCR_PROVIDER` is specified. Existing `OPENAI_API_KEY` and `OPENAI_MODEL` environment variables continue to work as before.

## Adding New Providers

New providers can be added by implementing the `providers.Provider` interface from the HTR tool and registering them in the provider service at `/workspace/hOCRedit/internal/providers/service.go`.