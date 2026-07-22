# Add a segmentor

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
provenance, scan the resulting image, and deploy its resolved digest.
