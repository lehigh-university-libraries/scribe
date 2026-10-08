# Configuration

Non-secret defaults live in `config.yaml` and the embedded copy under
`internal/config/defaults`. Environment interpolation supplies deployment
values. Cloud secrets come from Secret Manager; local secrets come from Vault or Compose secret files and must not be
placed in committed `.env` files, YAML, checked-in Terraform values, build
arguments, or image layers.

## PDF exports

The Compose `pdf-export` service runs Scyllaridae and the PDF command from the
same reviewed image digest as the API. It has no published port, credentials,
or persistent volumes. Only the API joins its internal network; the converter
cannot fetch images from the internet. The API's server-owned `pdf_export_url`
configuration defaults to `http://pdf-export:8080` (`PDF_EXPORT_URL` outside
Compose). It is never workspace input.

The runtime uses hash-locked `hocr-tools-lib` wheels from
`config/pdf/requirements.txt` and pinned Poppler tools. The command and all
repository integration remain Go/Bash. When updating the converter, review its
complete wheel hashes for both supported Alpine architectures and run
`make pdf-export-smoke`; accepting a PDF is insufficient without verifying
corrected Unicode text and the original images.

Temporary files live on a 1 GiB tmpfs. The service has a 1 GiB memory limit,
two CPUs, a 64-process limit, and a 128 MiB file-size limit. Successful and failed
commands remove their working directories; subsequent requests remove only
abandoned `scribe-pdf-*` directories older than two minutes, beyond the
85-second command deadline. Converter diagnostics and document text are not
included in logs. If exports fail, check `docker compose ps pdf-export` and
the service health check, then run `make pdf-export-smoke` against the reviewed
source before changing packages or resource limits.

## Application defaults

Runtime quota, storage, and IIIF-limit defaults are authored only in that
application config. Local Compose passes empty overrides so the application
selects them. Terraform decodes the same file, applies an explicitly supplied
operator override when present, and records the effective value for rollback.
The deployment workflow forwards non-empty repository variables without
embedding another fallback value.

The backend image copies `config.yaml` to `/etc/scribe/config.yaml`; local and
hosted Compose both consume that image-baked file and do not bind-mount a second
runtime copy. Rebuild the backend image after changing `config.yaml`. The
embedded copy remains the application fallback, and the repository contract
requires it to be byte-identical to the image source.

The model list in `config/ocr.yaml` is deployment/build metadata used to
construct and verify model images. It is not part of the embedded Go runtime
configuration; runtime provider and segmentor capabilities come from their
registered descriptors and endpoint policy.

The segmentor image packages Kraken BLLA for letters/manuscripts and
PP-DocLayoutV3 for newspaper layout reading order. Both artifacts are pinned
and checksum-verified during image builds. The native packaged inference CLIs
run behind the private segmentation service; model weights are never fetched
from workspace input. Transcription is restricted to GLM-OCR through Ollama,
Gemini, and OpenAI. The system default is **Letters + GLM-OCR**.

`generate-secrets.sh` creates high-entropy values for every locally owned
Compose secret that does not exist. The externally managed Google credential
mount is initialized only as an empty `{}` placeholder. Generated secrets
include the Triplet Presentation write token in
`secrets/triplet_presentation_write_token`, the constrained Triplet source-read
token in `secrets/triplet_source_read_token`, and the API-pagination HMAC key
in `secrets/page_token_signing_key`. Compose mounts each file only into the
services that consume it; none of these values is passed through Terraform,
GitHub Actions, image layers, or `.env`.

The local-to-dev-Cloud-Run OCR workflow uses keyless service-account
impersonation, not a downloadable service-account key. A reviewed dev Terraform
apply creates `scribe-dev-external`, grants explicit `user:` or `group:`
principals `roles/iam.serviceAccountTokenCreator` on only that account, and
grants the account `roles/run.invoker` on dev Kraken/segmentor services. The
configuration is inert outside the `dev` workspace. Terraform source changes
must be reviewed separately from an apply; this repository task does not grant
live IAM by itself.

After the IAM apply, contributors run:

```bash
GCLOUD_PROJECT=your-dev-project \
  scripts/configure-dev-cloud-ocr.sh configure
```

The helper follows [Google's supported local ADC impersonation
flow](https://cloud.google.com/docs/authentication/use-service-account-impersonation#auth-devel)
and writes
only the ignored `secrets/GOOGLE_APPLICATION_CREDENTIALS` file. Use `rotate` to
replace and revoke an old ADC, and `revoke` when access is no longer needed. The
file has no service-account private key, but its user refresh token remains
secret: never commit it, print it, attach it to a ticket, or put it in `.env`.
If it is lost, copied, or exposed, revoke it immediately. Removing the local
file alone does not revoke the underlying credential.

Cloud SQL uses a Private Service Connect endpoint and the Cloud SQL Auth Proxy with the service's
identity. The application database password is read from Secret Manager;
Terraform initializes the Cloud SQL user from the copied `database/app` secret.
Cloud Run never receives a service-account key. Local Compose still generates
its MariaDB root password in the ignored `secrets/mariadb_root_password` file.

All API replicas must use the same `pagination.signing_key` (normally loaded by
the entrypoint from `SCRIBE_PAGE_TOKEN_SIGNING_KEY_FILE`). Startup fails if the
key is missing, has surrounding whitespace, or is outside the 32–1024 byte
limit. Rotating the file and restarting the API and worker safely invalidates
outstanding `ListItems`, `ListTranscriptionJobs`, `ListContexts`, and
`ListSelectionRules` continuation tokens and five-minute prepared item-export
URLs. Pagination tokens are bound to the
workspace and exact filter; export tokens are additionally bound to the item,
format, and complete canonical revision-vector digest. The key signs token
integrity and is never emitted in logs or token payloads.

Authentication catalogs are intentionally bounded rather than configurable:
20 active sessions per user, 50 workspace memberships per user, 100 members per
workspace, 100 API keys per workspace, and 100 provider-secret locators per
workspace. Admission takes database parent-row locks, making these limits exact
across replicas. The worker removes expired sessions hourly in bounded batches;
new session creation also removes expired rows and evicts the oldest session at
the cap.

Provider secrets are scope-explicit. Workspace secrets require an administrator
and are the only credentials eligible for durable upload, reprocess, and worker
jobs. Personal secrets are available only to that user's foreground editor
enrichment calls; queued work never inherits the initiating user's credential.

Delete the Triplet token file and restart Triplet, the API, and the worker to
rotate that credential. Delete the pagination key file and restart its
consumers to rotate the pagination credential.
The generator records the host's numeric group as `SCRIBE_SECRETS_GID` in the
ignored local `.env`; Compose adds that supplementary group to non-root
services so mode `0640` secret files work without root or world-readable bits.

Important groups include:

- public URL and CORS origins;
- MariaDB and persistence paths;
- Vault address, workspace, auth role, and secret paths;
- registered provider/segmentor endpoints and audiences;
- IIIF public and internal origins;
- queue backend, lease, retry, and retention settings;
- OpenTelemetry exporter, sampling, export, and queue-poll bounds;
- request, upload, image-pixel, rate, and stream limits;
- metadata-only provider-call audit retention.

`public_base_url` must be an absolute HTTP(S) URL without credentials, query,
or fragment. Startup also verifies that the longest possible Scribe
Annotation child ID fits the 512-byte persisted identity columns. An overlong
base fails configuration loading rather than producing runtime ingest errors.

The `observability` group is non-secret and deliberately does not accept an
export endpoint. `exporter` (or `SCRIBE_OTEL_EXPORTER`) is either `none` or
`google`; local defaults use `none`, while managed Terraform selects `google`
for dev and production, sets `GOOGLE_CLOUD_PROJECT`, and leaves PR previews
disabled. `deployment_environment` (`SCRIBE_DEPLOYMENT_ENVIRONMENT`) is a
bounded `local`, `dev`, `prod`, or `preview` resource label supplied by the
deployment rather than request data. The Google exporter uses the same mounted ADC
as other application GCP clients and sends only to the fixed Cloud Monitoring
and Cloud Trace APIs. Do not put credential JSON, authorization headers, or
collector URLs in this group.

`metric_export_interval` (`SCRIBE_OTEL_METRIC_EXPORT_INTERVAL`) and
`queue_poll_interval` (`SCRIBE_OTEL_QUEUE_POLL_INTERVAL`) default to `60s` and
`30s`; each accepts `10s` through `5m`. `export_timeout`
(`SCRIBE_OTEL_EXPORT_TIMEOUT`) defaults to `10s` and accepts `1s` through
`30s`. `trace_sample_ratio` (`SCRIBE_OTEL_TRACE_SAMPLE_RATIO`) defaults to
`0.05` and accepts `0` through `1`; zero disables server-span sampling. The API
identifies itself as
`scribe-api`; only `scribe-worker` polls the claimable jobs table. Telemetry
startup and runtime failures are non-fatal and are excluded from readiness.

Each `auth.external_jwt_issuers` entry must pin `issuer`, `audience`,
`workspace_id`, and `service_user_id`. The service user is a dedicated internal
Scribe account that must remain a member of that exact workspace; membership is
rechecked for every token. External `sub` and `webid` claims are provenance
only and are never interpreted as database user IDs, item owners, or provider-
secret selectors. Give the dedicated account only the workspace role and
provider credential needed by the integration, then further narrow access with
the issuer role-to-scope mappings.

Triplet is the only IIIF Image API implementation. `iiif.internal_base` points
to Triplet, while `iiif.source_base` must be the exact Scribe
`/static/uploads` collection that Triplet may dereference. The
`TRIPLET_SOURCE_READ_TOKEN` secret grants only immutable GET/HEAD access to
that collection; it cannot authorize a Connect or business route. Compose
generates `secrets/triplet_source_read_token` and mounts it into the API and
worker without putting the value in `.env`.

Triplet Presentation publication is a required, fail-closed boundary. Configure
`TRIPLET_PRESENTATION_BASE`, `TRIPLET_PRESENTATION_INTERNAL_BASE`, and
`TRIPLET_PRESENTATION_WRITE_TOKEN` together. Both bases must be absolute
HTTP(S) URLs whose path is exactly `/presentation/v3`; the public base must use
HTTPS except for loopback development. The write token must contain 32–1024
bytes with no surrounding whitespace. Startup rejects an empty or partial
group, so Scribe cannot accept a publication while silently fabricating an ID
that no Presentation server owns.

Forwarding headers are fail-closed. Local Compose trusts only its Traefik peer
through Docker service discovery. In Cloud Run, the frontend requires HTTPS
and the platform's single client address, applies the configured allowlist,
and forwards that canonical address to the API over loopback. The API trusts
only loopback proxies. Cloud Run containers in one service share its service
account; the frontend, API, Triplet, PDF renderer, and SQL proxy are one identity
boundary. The worker and OCR services use separate identities.

Cloud deployments set `SECRET_MANAGER_PROJECT_ID` and `SECRET_MANAGER_PREFIX`.
The secret adapter hashes logical workspace paths into deployment-specific
secret names. Provider payloads are opaque JSON objects; neither values nor
logical paths are printed by the copy command. See
[deployment](deployment.md) for `make secret-manager-secrets` and the bootstrap
sequence. API and worker replicas share the generated pagination and Triplet
tokens through Secret Manager. Triplet stores Presentation resources in its
own Cloud SQL database; its local image cache is disposable.

`SEGMENTOR_MAX_CONCURRENCY` (default `1`, maximum `8`) bounds model work per
process while leaving health probes unqueued. Hosted OCR services also admit
exactly one request per Cloud Run instance, matching that process limit so
pressure scales to another bounded instance instead of forming an invisible
queue behind one native model call. Keep both limits equal; increase them only
with measured CPU and memory headroom. Triplet owns its own transform
concurrency and decoded-image limits in `triplet.config.yaml`.

`iiif.max_manifest_canvases` (or `IIIF_MAX_MANIFEST_CANVASES`) defaults to
`500` and may be configured from `1` through `5000`. Manifest import counts
declared Presentation 2 or Presentation 3 Canvases immediately after the
bounded fetch and JSON decode. An oversized manifest is rejected before an
idempotency reservation, item, image, OCR run, or transcription job is written.
Increase the limit only after accounting for the database and queue fan-out of
one request.

`iiif.max_manifest_import_bytes` (or `IIIF_MAX_MANIFEST_IMPORT_BYTES`) bounds
the combined retained Presentation 3 source bytes and prefetched hOCR for one
import, up to the 64 MiB hard ceiling. The raw source Manifest itself has a
separate fixed 20 MiB ceiling. Both checks run before tenant persistence, and
retained bytes count toward database storage quotas.

Manifest and hOCR fetches use fixed media-type preferences and a fixed Scribe
importer identity. Idempotent GETs retry at most three times for transport
failures and HTTP 408, 425, 429, 500, 502, 503, or 504 responses, honoring a
bounded `Retry-After`. Retries remain inside the aggregate manifest-import
deadline and do not relax SSRF, redirect, byte, Canvas-count, or concurrency
limits. Other upstream responses fail without retry.

`transcription.max_active_jobs_per_workspace` (or
`TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE`) defaults to `1000` and accepts
values from `1` through `100000`. It is a durable database admission limit over
pending and running jobs, not a per-process concurrency limit. Admissions take
a workspace-scoped database lock, so concurrent API instances cannot overfill
one tenant's queue; terminal jobs release capacity automatically.

The `storage` group applies durable admission limits before uploads, provider
calls, item creation, or manifest fan-out. Defaults are 5 GiB, 5,000 items, and
10,000 images per workspace, with global ceilings of 30 GiB, 50,000 items, and
100,000 images. Capacity is derived from canonical `items`, `item_images`, and
the durable upload-cleanup outbox. `item_images.storage_bytes` records the final
persisted image length. Deleting a canonical row atomically transfers those
bytes to its cleanup record; capacity is released only after the immutable blob
has been deleted successfully. Live and pending references are grouped by blob
identity so a shared object is counted once globally. Transactionally
maintained workspace/global usage counters provide admission serialization;
they are rebuildable materializations of canonical rows and cleanup records,
not a second ownership source. Provider-call audits persist identifiers,
timing, status, and bounded categorical errors only. Prompts and provider
request/response bodies are never captured. Every audit row is
workspace-scoped and charged at least 512 durable database bytes; retention
and item deletion release that accounting transactionally.
Short-lived database reservations cover work before the canonical transaction
commits and serialize admission across API replicas. Reservations default to a
six-hour TTL and are removed after success, compensation, or expiry following a
crash. Once an immutable name is known, byte accounting is atomically bound to
that exact cleanup identity before either the local or shared blob write. A
process crash therefore leaves a discoverable, quota-accounted object for the
cleanup worker instead of an untracked orphan. Both per-workspace and global
ceilings must leave headroom for database,
normalization-cache, and temporary processing data on the shared disk.
The content-addressed Houdini normalization cache is independently pruned to
2 GiB and seven days by default (`normalization_cache_max_bytes` and
`normalization_cache_max_age`); cache eviction is safe because normalized
images are reproducible derivatives, not canonical uploads.

The API and background worker apply one hierarchical processing limit before
expensive model work. `processing.global_concurrency`,
`per_workspace_concurrency`, and
`per_provider_concurrency` default to `4`, `2`, and `2`. Synchronous whole-page
enrichment is rejected before any provider call when it exceeds
`max_page_enrichment_lines` (default `500`, hard ceiling `500`). Durable jobs
likewise reject a segmented page before credential lookup or provider work when
it exceeds `transcription.max_segments_per_job` (default `500`, hard ceiling
`500`). Background jobs use only workspace-scoped provider credentials; a
workspace member cannot cause a job to spend another member's user-scoped key.
Waiting is cancellable,
and a request never holds one quota while waiting for another, so a busy tenant
or transcription provider cannot consume every process slot. Queue, recovery,
and local-poll workers carry workspace ownership explicitly from the durable
job; they do not depend on an HTTP principal. `external_request_retention`
defaults to 720 hours; terminal and abandoned idempotency records older than
that are removed in bounded batches, while live leases are always preserved.
Reservations tied to an item image are removed immediately when that resource
is deleted.

Per-process fan-out is validated at startup as well. `transcription.job_workers`
defaults to `3` and is limited to `32`; Pub/Sub outstanding messages default to
that worker count and are capped at `128`. Independent line transcription defaults to `5`
concurrent calls and is capped at `32`. Invalid negative or oversized values
fail startup instead of creating an unbounded worker pool.

Startup validates required settings and fails closed. Provider body capture is
off by default. Authenticated endpoint origin and audience are an
administrator-owned pair; a workspace cannot supply either value.

Provider-secret metadata is a durable cross-system lifecycle record. Creation
commits a workspace-scoped `pending_write` locator before writing the secret store and
changes it to `active` only after the secret store succeeds. Only `active` rows can be
listed or resolved for a provider call. Failed creates and explicit deletes
are state-fenced into `cleanup_pending`; the worker retries idempotent secret-store
deletion before removing the locator. It also reconciles interrupted
`pending_write` rows after a bounded grace period. Scribe has no database
foreign keys: any future user/workspace deletion use case must first drive all
provider-secret locators through this application lifecycle, and direct SQL
parent deletion is unsupported. Run the worker whenever provider-secret
storage is enabled.

Production IIIF Image API traffic is served by Triplet. Internal Scribe image
operations send an absolute raw-source URL and the constrained source token to
Triplet; Triplet forwards that credential only while dereferencing the exact
immutable source. Public Triplet requests carry no source token, so Scribe's
normal owner/publication checks remain in force. The frontend treats `/iiif`
and `/presentation` routes as read-only. It sends Presentation reads to the backend origin unless
`SCRIBE_FRONTEND_PRESENTATION_ORIGIN` is set explicitly, as it is for local
Compose's direct Triplet connection. Canonical writes continue through the
authenticated Connect API.

When adding configuration, update both YAML copies, parsing/validation tests,
the Terraform/Compose wiring where applicable, and this page.

Every OCR model with a DOI must also declare its exact `sha256` in the `ocr`
model registry. The OCR matrix passes that digest into the image build, and the
installer verifies the downloaded model before copying it into the runtime
image. The Vault image similarly pins the official HashiCorp image digest;
version bumps must update the version and digest together.
