# Laypa Integration Setup

hOCRedit now uses [Laypa](https://github.com/knaw-huc/laypa) for document segmentation and baseline detection instead of custom word detection algorithms. Laypa is a **required dependency**.

## Architecture

The system now consists of two services:
1. **hOCRedit**: Web interface and LLM transcription service
2. **Laypa**: Document segmentation service (provides bounding boxes and baselines)

## Quick Start with Docker Compose

### Prerequisites

1. Download a Laypa model from the [pretrained models](https://surfdrive.surf.nl/files/index.php/s/YA8HJuukIUKznSP?path=%2Flaypa)
2. Create a `laypa_models` directory structure:
   ```bash
   mkdir -p laypa_models/default
   ```
3. Place your model files in `laypa_models/default/`:
   - `config.yml` - Laypa configuration file
   - `model_final.pth` - Trained model weights

### Running the Services

```bash
# Start both services
docker-compose up

# Or run in detached mode
docker-compose up -d
```

The services will be available at:
- **hOCRedit**: http://localhost:8888
- **Laypa API**: http://localhost:5000

## Configuration

### Environment Variables

#### hOCRedit Service

- `LAYPA_API_URL`: URL of the Laypa API service (default: `http://laypa:5000`)
- `LAYPA_MODEL_NAME`: Model folder name in Laypa (default: `default`)
- `OCR_PROVIDER`: LLM provider (openai, azure, gemini, ollama)
- Provider-specific variables (OPENAI_API_KEY, OLLAMA_HOST, etc.)

#### Laypa Service

- `LAYPA_MODEL_BASE_PATH`: Base path for models (default: `/models`)
- `LAYPA_OUTPUT_BASE_PATH`: Output directory (default: `/output`)
- `LAYPA_MAX_QUEUE_SIZE`: Maximum queue size (default: 128)
- `GUNICORN_WORKERS`: Number of workers (default: 1)
- `GUNICORN_THREADS`: Number of threads per worker (default: 1)

### GPU Support

To enable GPU support for Laypa, uncomment the GPU configuration in `docker-compose.yml`:

```yaml
deploy:
  resources:
    reservations:
      devices:
        - driver: nvidia
          count: 1
          capabilities: [gpu]
```

**Note**: Requires [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html)

## Model Setup

### Model Directory Structure

```
laypa_models/
└── default/              # Model name (matches LAYPA_MODEL_NAME)
    ├── config.yml       # Laypa configuration
    └── model_final.pth  # Model weights
```

### Multiple Models

You can configure multiple models:

```
laypa_models/
├── handwritten/
│   ├── config.yml
│   └── model_final.pth
└── printed/
    ├── config.yml
    └── model_final.pth
```

Set `LAYPA_MODEL_NAME` to switch between models.

## API Integration

hOCRedit communicates with Laypa via REST API:

### Laypa API Endpoints

- `POST /predict` - Submit image for segmentation
  - Form data: `image` (file), `identifier` (string), `model` (string)
  - Returns: PageXML with text regions and baselines

- `GET /health` - Health check endpoint
- `GET /status_info/<identifier>` - Check request status
- `GET /queue_size` - Current queue size

### Data Flow

1. User uploads image to hOCRedit
2. hOCRedit sends image to Laypa API
3. Laypa performs segmentation and returns PageXML
4. hOCRedit converts PageXML to internal format
5. hOCRedit sends text regions to LLM for transcription
6. Results displayed as editable hOCR

## Development

### Running Without Docker Compose

#### Start Laypa Separately

```bash
cd laypa
conda activate laypa
python api/gunicorn_app.py
```

#### Configure hOCRedit

```bash
export LAYPA_API_URL=http://localhost:5000
export LAYPA_MODEL_NAME=default
export OCR_PROVIDER=ollama
export OLLAMA_HOST=http://localhost:11434
export OLLAMA_MODEL=mistral-small3.2:24b

go run main.go
```

### Testing Laypa Connection

```bash
# Check Laypa health
curl http://localhost:5000/health

# Check queue size
curl http://localhost:5000/queue_size

# Submit test image
curl -X POST http://localhost:5000/predict \
  -F image=@test_image.jpg \
  -F identifier=test123 \
  -F model=default
```

## Troubleshooting

### Laypa Not Available

If you see: `Laypa service is required but not available`

1. Check Laypa service is running:
   ```bash
   docker-compose ps laypa
   ```

2. Check Laypa health:
   ```bash
   curl http://localhost:5000/health
   ```

3. Check Laypa logs:
   ```bash
   docker-compose logs laypa
   ```

### Model Not Found

If Laypa returns model errors:

1. Verify model directory exists:
   ```bash
   ls -la laypa_models/default/
   ```

2. Check model files are present:
   - `config.yml`
   - `model_final.pth` (or model specified in config)

3. Verify model path in docker-compose.yml volume mount

### Performance Issues

- **Slow inference**: Consider enabling GPU support
- **Out of memory**: Reduce `GUNICORN_WORKERS` or enable AMP in Laypa config
- **Queue full**: Increase `LAYPA_MAX_QUEUE_SIZE`

## Architecture Details

### PageXML to hOCR Conversion

Laypa outputs PageXML format, which hOCRedit converts to internal format:

- PageXML `TextRegion` → hOCR `Block`
- PageXML `TextLine` → hOCR `Word`
- PageXML `Baseline` → Used for line detection
- Coordinates preserved for visual overlay

### Removed Components

The following custom segmentation code has been removed:

- ❌ Custom word detection (flood fill algorithm)
- ❌ Connected component analysis
- ❌ Line grouping heuristics
- ❌ ImageMagick preprocessing for segmentation

✅ All segmentation now handled by Laypa

## References

- [Laypa GitHub](https://github.com/knaw-huc/laypa)
- [Laypa Paper](https://doi.org/10.1145/3604951.3605520)
- [Pretrained Models](https://surfdrive.surf.nl/files/index.php/s/YA8HJuukIUKznSP?path=%2Flaypa)
- [PageXML Format](https://github.com/PRImA-Research-Lab/PAGE-XML)
