# Add a segmentor

Use this guide for a new engine or wire contract. To add another model served
by an existing segmentor, follow
[add a segmentation model](adding-segmentation-model.md) instead.

1. Implement `/v2/segment` and `/v2/capabilities` using
   `github.com/lehigh-university-libraries/htr/pkg/segment/remote`, then register the
   Scribe capability descriptor and approved model routes. Do not add a second
   multipart HTTP client in Scribe.
   Older services may use the existing v1 protocol; fallback occurs only on
   404 or 405. Other errors never trigger another provider call.
2. Define accepted image formats, pixel limits, model keys, output granularity,
   and coordinate system.
3. Normalize its output into IIIF annotations in backend domain code.
4. Preserve Unicode text and document-language behavior; do not apply an
   English ASCII word filter globally.
5. Test rotations, large dimensions, empty output, overlapping regions,
   provider timeouts, and retry safety.

External helper services remain private. Their URL and identity-token audience
come from administrator configuration and must match an exact registered
origin. Use Scribe's process-wide `internal/gcpidentity` source for Cloud Run
authentication; workspace/context input may never supply either value. A
configured `GOOGLE_APPLICATION_CREDENTIALS` file must be either an exact service
account credential using Google's standard token endpoint or Scribe's validated,
non-delegated `impersonated_service_account` ADC shape. The latter accepts only
an `authorized_user` source and Google's fixed OAuth and IAM endpoints. Invalid
configured credentials fail closed; HTR's metadata source is used only when the
credential file setting is absent. Configure each audience as a canonical HTTPS
origin without a trailing slash or path.

The local helper serves `kraken`, `ppdoclayout`, and `layout+kraken`;
`newspapers` remains the existing layout-plus-lines alias. The layout model
directory and selection metadata are in `config/ocr.yaml`. PP-DocLayout alone
returns regions and is available for layout evaluation; transcription contexts
require line geometry and should use `layout+kraken`.

To enable `textract` or `azure-layout`, register the model key under
`segmentation_service.models` and its exact private v2 endpoint in
`SEGMENTATION_MODEL_ENDPOINTS_JSON`. That service owns cloud credentials and
uses htr's grounded parser clients. Scribe sends the resolved document type as
`hints.documentType`; item and context requests never choose an endpoint.

Kraken model entries in `config/ocr.yaml` use an immutable version DOI and an exact
basename ending in `.mlmodel`. The image build fails if that named file is not
present in the DOI download; it never guesses another cached model. Treat a
model change like a dependency update: review the source, rebuild with SBOM and
provenance, and deploy its resolved digest. Runtime image vulnerability scanning
is currently deferred and does not gate CI or deployment.
