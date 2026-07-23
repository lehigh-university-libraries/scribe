# Add a segmentation model

A segmentation model is a selectable model route implemented by an installed
segmentor. Do not add another transport client for each model. If the engine or
wire contract is new, first follow [add a segmentor](adding-segmentor.md); if
the existing remote Kraken service can execute the model, extend its model
registry instead.

## Register a Kraken model

1. Add a stable selection key under
   `config/ocr.yaml` `kraken.segmentation_models`.
2. Set `file` to the exact `.mlmodel` basename and record the immutable DOI and
   lowercase SHA-256. The installer must find that exact file and verify its
   bytes. The stable public selection key may differ from this baked filename.
3. Change `kraken.default_segmentation_model` only when this should become the
   model bundled into the generic segmentor and used by readiness. For a
   default change, also update the matching `KRAKEN_SEGMENTATION_MODEL_*`
   defaults in `Dockerfile.segmentor` and the model key in both local
   `SEGMENTATION_MODEL_ENDPOINTS_JSON` maps in
   `docker-compose.override-example.yaml`.
4. Add the same selection key to `segmentation_service.models` in both
   `config.yaml` copies for local/default discovery.
5. Run:

   ```bash
   GCLOUD_PROJECT=scribe-test \
     WORKSPACE_SLUG=prod \
     IMAGE_TAG=0123456789abcdef \
     make ocr-matrix
   ```

   It should emit a
   `kraken-seg/<selection-key>` image entry. Terraform creates the corresponding
   private service and derives `SEGMENTATION_MODELS_JSON` plus
   `SEGMENTATION_MODEL_ENDPOINTS_JSON` from the reviewed registry.
   A route-specific segmentation image contains only its selected segmentation
   artifact and bakes the public model ID separately from the filename. A
   route-specific transcription build likewise fetches only its selected
   recognition artifact and consumes already-cropped lines. The generic paired
   segmentor fetches the defaults for both operations. None accepts a
   runtime-selectable model map.
6. Add registry tests proving the key is cataloged and resolves only to its
   exact administrator-owned endpoint. Unknown keys must be rejected.
7. Test empty pages, rotations, large dimensions, overlapping regions,
   timeouts, Unicode handoff, and retry safety. Verify returned geometry is
   normalized into the same canonical IIIF AnnotationPage used by the editor
   and exporters.

The selection key is the value stored in a context. Keep it stable even if a
service name or region changes. Cloud Run names and audiences are deployment
details and must never appear in a context request.

## Local development

The default Compose helper contains the configured default Kraken segmentation
artifact. To exercise another packaged artifact locally, build the segmentor
image with that model's reviewed DOI, basename, and SHA-256, then register its
selection in the server-owned segmentation model and endpoint environment.
Update both API and worker wiring together.

`make ocr-matrix-test` checks that the Dockerfile defaults and both local
endpoint maps still match `config/ocr.yaml`.

Do not silently route an unavailable model to `auto`. A local setup that cannot
execute the new selection should fail clearly or omit it from its runtime
catalog.

## Completion checklist

- Runtime and deploy registries use the same stable selection key.
- The exact artifact is digest-verified during the image build.
- Catalog discovery exposes no URL or audience.
- API and worker resolve the same endpoint.
- Model-specific geometry and error cases have automated tests.
- A real readiness path observes non-empty model output.
