# Add a transcription model

Adding a model to an installed provider is smaller than adding a provider.
Keep transport, credential shape, retry policy, and endpoint policy in the
existing `internal/providerregistry` descriptor. Add a new provider only when
those semantics differ; see [add a transcription provider](adding-provider.md).

There are two registries with different jobs:

- `config.yaml` and `internal/config/defaults/config.yaml` define the runtime
  allowlist and default exposed by `ContextService.GetModelCatalog`.
- `config/ocr.yaml` defines immutable images and deployment routes for
  Scribe-hosted Ollama and Kraken models.

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

## Scribe-hosted Kraken recognition model

For Kraken transcription:

1. Add a key under `config/ocr.yaml`
   `kraken.transcription_models`. Its `file` must be an exact `.mlmodel`
   basename and its DOI and lowercase SHA-256 must identify the reviewed
   artifact. The stable public key stored in contexts may differ from the
   baked filename.
2. Add the same key to `llm.kraken.models` in both runtime configuration
   copies. Set `kraken.default_transcription_model` and `llm.kraken.model`
   together only when changing the default. For a default change, also update
   the matching `KRAKEN_TRANSCRIPTION_MODEL_ID` and
   `KRAKEN_RECOGNITION_MODEL_*` defaults in `Dockerfile.segmentor` and the
   model key in both local `KRAKEN_MODEL_ENDPOINTS_JSON` maps in
   `docker-compose.override-example.yaml`.
3. Run the `make ocr-matrix` command shown above and verify it emits
   `kraken-ocr/<key>`. The build must fail when the DOI download does not
   contain the exact basename or its bytes do not match the configured digest.
   The installer first resolves the DOI through Kraken/HTRMoPo. If that catalog
   does not index the reviewed model, it downloads only the configured basename
   from the exact `10.5281/zenodo.<record>` record over HTTPS. Both paths must
   pass the same pinned SHA-256 check before the artifact is published.
   The image bakes the public key and filename as separate values and accepts
   only that exact transcription key at runtime. The dedicated route fetches
   only its selected recognition artifact and does not configure a segmentation
   route: Scribe sends already-cropped lines, and Kraken recognizes each crop
   with `ocr --no-segmentation`.
4. Exercise `providerregistry` model routing, the remote OCR client contract,
   and a worker job that commits against the expected canonical page revision.
5. Verify the protected deployment supplies matching `KRAKEN_MODELS_JSON` and
   `KRAKEN_MODEL_ENDPOINTS_JSON`, then run a real transcription readiness
   probe.

Never place an endpoint or audience in a context. Local development can provide
the server-owned model endpoint environment documented in
[configuration](../operations/configuration.md); production Terraform owns it.
`make ocr-matrix-test` checks that the Dockerfile defaults and both local
endpoint maps still match `config/ocr.yaml`.

## Completion checklist

- The model appears once in `GetModelCatalog` under the intended provider.
- An unknown model is rejected instead of falling back.
- The provider default is unambiguous.
- Hosted artifacts and container inputs are immutable and verified.
- Provider failures remain typed and redact credentials and response bodies.
- A queued attempt is fenced by lease token and input revision.
- The relevant user and operator documentation names when the model should be
  selected.
