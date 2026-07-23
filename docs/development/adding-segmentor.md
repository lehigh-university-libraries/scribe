# Add a segmentor

Use this guide for a new engine or wire contract. To add another model served
by an existing segmentor, follow
[add a segmentation model](adding-segmentation-model.md) instead.

1. Implement the general `/v1/segment` or `/v1/transcribe` transport in
   `github.com/lehigh-university-libraries/htr/pkg/remoteocr`, then register the
   Scribe capability descriptor and approved model routes. Do not add a second
   multipart HTTP client in Scribe.
2. Define accepted image formats, pixel limits, model keys, output granularity,
   and coordinate system.
3. Normalize its output into IIIF annotations in backend domain code.
4. Preserve Unicode text and document-language behavior; do not apply an
   English ASCII word filter globally.
5. Test rotations, large dimensions, empty output, overlapping regions,
   provider timeouts, and retry safety.

External helper services remain private. Their URL and identity-token audience
come from administrator configuration and must match an exact registered
origin. Use HTR's cached `gcpidtoken` source for Cloud Run authentication;
workspace/context input may never supply either value.

Kraken model entries in `config/ocr.yaml` use an immutable version DOI and an exact
basename ending in `.mlmodel`. The image build fails if that named file is not
present in the DOI download; it never guesses another cached model. Treat a
model change like a dependency update: review the source, rebuild with SBOM and
provenance, and deploy its resolved digest. Runtime image vulnerability scanning
is currently deferred and does not gate CI or deployment.
