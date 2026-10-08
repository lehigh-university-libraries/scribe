# Add a transcription model

Adding a model to an installed provider is smaller than adding a provider.
Keep transport, credential shape, retry policy, and endpoint policy in the
existing `internal/providerregistry` descriptor. Add a new provider only when
those semantics differ; see [add a transcription provider](adding-provider.md).

There are two registries with different jobs:

- `config.yaml` and `internal/config/defaults/config.yaml` define the runtime
  allowlist and default exposed by `ContextService.GetModelCatalog`.
- `config/ocr.yaml` defines immutable images and deployment routes for
  Scribe-hosted GLM-OCR models through Ollama.

A model is usable only when the runtime catalog and its execution path agree.

## Vendor-hosted model

For an existing fixed vendor adapter such as OpenAI or Gemini:

1. Confirm the vendor still serves the exact identifier and that the existing
   adapter accepts its request and response shape.
2. Add the identifier to that provider's `models` list in both runtime
   configuration copies.
3. Change `model` only when the new identifier should become that provider's
   default. A default is included in the normalized allowlist, but declaring it
   explicitly keeps configuration review clear.
4. Extend registry and adapter tests for catalog discovery, canonical
   case-insensitive selection, limits, redacted errors, and concurrent
   credential isolation.
5. Run the focused configuration, registry, provider, worker, and generated
   contract tests.

Do not add the model to `config/ocr.yaml`: Scribe does not build or host fixed
vendor models.

## Scribe-hosted Ollama model

An Ollama addition also creates an immutable build and Cloud Run route:

1. Add the model to `config/ocr.yaml` under `ollama.models` with the exact
   upstream manifest digest. Keep the base image tag and digest reviewed
   together.
2. Add the same model identifier to `llm.ollama.models` in both runtime
   configuration copies. To make it the default, change
   `config/ocr.yaml` `ollama.default_model` and `llm.ollama.model` in both
   runtime copies together. CI requires the deploy default to be a declared
   model and to match the runtime default.
3. Run:

   ```bash
   GCLOUD_PROJECT=scribe-test \
     WORKSPACE_SLUG=prod \
     IMAGE_TAG=0123456789abcdef \
     make ocr-matrix
   ```

   Confirm it emits one `ollama/<model>` entry with a stable service name and
   exact build arguments.
4. Run the OCR matrix/build contracts and registry tests. The protected build
   workflow publishes the image and Terraform derives
   `OLLAMA_MODELS_JSON` and `OLLAMA_MODEL_ENDPOINTS_JSON` from the reviewed
   model set; do not hand-maintain a second production endpoint map.
5. Deploy through the normal protected path and verify a non-empty
   transcription, not only Cloud Run health.

Previews reuse reviewed main OCR images. A pull request that refers to a model
not yet present in the protected base cannot prove that new hosted model in a
preview; deploy the reviewed main model image before relying on it.
