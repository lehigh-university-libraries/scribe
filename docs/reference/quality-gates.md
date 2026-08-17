# Quality gates

Required pull-request checks cover:

- gofmt, ShellCheck, actionlint, golangci-lint, and Buf lint;
- generated Go, TypeScript, sqlc, and OpenAPI drift;
- Go race tests, DB integration tests, release-target 32-bit cross-compilation,
  and web/plugin tests and builds;
- real Chromium editor acceptance tests in a pinned Playwright container;
- OCR build tags and DB-backed ingest/revision acceptance tests;
- isolated backup/restore integrity and expired-job recovery smoke tests;
- gosec, reachable Go vulnerability analysis, npm audits, and Trivy dependency
  plus credential scanning with a synthetic detection regression;
- hash-locked segmentor Python transitives, repository dependency and secret
  scanning, digest-pinned runtime images, and packaged-runtime smoke tests;
- Terraform formatting/init/validation, moved-state and targeted-plan tests,
  rendered Compose checks, actionlint, and fake-driven deployment, identity,
  rollback, readiness, backup, and cleanup behavior;
- production source-lineage fixtures that accept ordinary descendant deploys
  and only the exact same-slot forced-amend retry, while rejecting mismatched
  event SHAs, force flags, subjects, parents, and commit shapes;
- executable WIF fixtures that reject broader repository, workflow, ref,
  environment, issuer, mapping, pool, or service-account bindings, followed by
  the same fail-closed inspection against live GCP in every protected workflow;
- release-helper fixtures with a fake Git/GitHub retry proving a later main
  HEAD is rejected, the requested numeric version is exact, a partial publish
  reuses the unmoved tag and draft, downloaded Linux/Darwin/Windows assets
  match their checksums, a completed release is a
  no-op, and an out-of-order older merge skips only after a later exact release
  is complete;
- a fake-Docker destructive-operation contract proving the local database reset
  refuses missing confirmation, CI, mislabeled resources, and broad volume removal;
- Zensical documentation build.

`make ci` is the local entrypoint and includes the same Trivy high/critical
dependency and secret scan as the hosted workflow. Runtime image scanning is
currently deferred and does not gate CI, deployment, or release. Individual
component commands remain useful for iteration, but a manually checked box is
not a substitute for a passing required job. `ci/run-ci.sh` owns the canonical
`contracts`, `test`, `browser`, `recovery`, `security`, and `infrastructure`
groups; hosted jobs call those same groups in parallel while `make ci` runs
them locally. The orchestrator creates a unique Compose project from the
reviewed base file (never a developer's local override), lets Docker allocate a
collision-free bridge, waits for MariaDB health before integration tests, and
removes its containers and volumes on success, failure, or interruption. It
does not reuse or stop the normal development stack, so integration tests
cannot silently skip on a clean checkout.

`make ocr-build-tags` cross-compiles both GoReleaser binaries for `linux/386`
with the release build tag in the same pinned Go container used for its native
build checks. Hosted CI and the local entrypoint therefore reject constants and
other code that compile on 64-bit development hosts but fail a supported
release target.

Every Scribe-managed GCP VM, including previews and production, uses
Container-Optimized OS as the sole host-runtime standard. Terraform-installed
host scripts therefore use the jq feature set shipped by COS and may not depend
on its unavailable Oniguruma regex functions or jq 1.6's broken
`contains("\\u0000")` behavior. Container images and CI-only scripts retain
their independently pinned toolchains.

`make segmentor-lock-check` proves every Python requirement is exact and every
accepted distribution has a SHA-256 hash, including the explicitly retained
unsafe `setuptools` transitive. `make segmentor-lock` regenerates that file in
the digest-pinned Python image with an exact `pip-tools` version. Release images
install it with both `--require-hashes` and `--only-binary=:all:`.

The code-generation, documentation, and security entrypoints also inspect host
tool versions before execution: Buf 1.72.0, sqlc v1.31.1, Zensical 0.0.51,
gosec module v2.28.0, and govulncheck module v1.6.0. Fixture tests independently
prove that each unreviewed version fails before the tool can operate.

`make docs-build` is the one strict documentation build path used by local CI,
the hosted infrastructure/documentation job, and GitHub Pages. `make docs`
remains a convenience alias. The Pages workflow uploads the same ignored
`site/` directory produced locally rather than maintaining another build
recipe.

The hosted browser job independently starts MariaDB and sets
`SCRIBE_REQUIRE_BROWSER_BACKEND=true`; it cannot silently select in-browser
persistence. The CI workflow no longer runs separately on `main` because the
production workflow invokes the same reusable jobs for that SHA. Pull requests
still run the direct CI workflow, and same-repository previews run the same gate
before any image build or credentialed deployment. Preview head images are
built and smoked as credential-free OCI artifacts; a protected publisher job
is the first step allowed to authenticate to the registry and it never executes
the pull-request checkout.

The backend jobs likewise set `SCRIBE_REQUIRE_TEST_DB=true`; failure to resolve
the isolated Compose database is a gate failure rather than a unit-only pass.
The full Go suite owns required DB acceptance coverage, while `make e2e-smoke`
is the focused subset for local ingest/revision iteration and is not rerun in
the same required job.

Because trusted `pull_request_target` orchestration is associated with the
protected base revision, the preview workflow records separate deployment
evidence against the exact tested PR head. That no-checkout job revalidates the
live PR, disables auto-merge, marks the deployment transient and non-production,
and can report `success` only after the managed revision and public readiness
checks pass. Failed reruns supersede same-PR successes with terminal evidence;
stale runs cannot approve a newer head, and successful teardown marks that PR's
records inactive. `make preview-deployment-test` exercises typed input
resolution and immutable teardown/recovery behavior with isolated fakes.

`make test-browser` runs Chromium against a Vite harness that imports the
production editor shell, OpenSeadragon geometry functions, editor-session
reducer, annotation adapter, and a fully mounted Mirador/Scribe viewer with a
two-Canvas IIIF fixture. It covers dialog focus/keyboard routing,
offset/scroll/zoom coordinate conversion, dirty-draft background rebasing, and
save/reload/revision-conflict behavior, editable shortcut safety, and active
Canvas event/persistence routing. The standalone persistence fixture is an
in-browser CAS service for fast iteration. In `make ci`, Playwright instead calls the
generated Connect client through Vite's production proxy configuration and a
real handler backed by the isolated MariaDB project; CI requires that boundary
and fails if it is unavailable. Tenant isolation, ingestion, and job recovery
remain covered by their focused integration and smoke suites.

On an empty Go module cache, compiling the browser fixture can take longer than
the editor scenarios themselves. `ci/test-browser.sh` therefore waits up to 600
seconds for the fixture by default. Set
`SCRIBE_BROWSER_BACKEND_READY_TIMEOUT_SECONDS` to an integer from 30 through
900 when a slower or faster runner needs a different bounded startup budget.

`make backup-restore-smoke` creates isolated source and restore databases plus
blob volumes, migrates the source through the real embedded migrator, restores
the ledger, dump, and upload archive, reruns migration validation, verifies
canonical IIIF and derived-index integrity, checks the blob hash, and confirms
expired job leases recover. Its temporary containers, network, volumes, and
files are removed by an exit trap.

`make cloud-snapshot-restore-drill-test` covers fresh/source-matched selection
for the production data and Compose-volume disks; read-only/no-egress
inspection of both the crash-consistent volume state and the completed logical
dump captured on the data disk; completion-marker and schema checks; safe
label/provenance-gated cleanup; stale resources; and partial-failure cleanup.

`make ocr-matrix-test` executes the OCR matrix generator against valid and
invalid structured catalogs without starting a build container. It rejects
missing artifacts, unknown defaults, route drift, model-file collisions, and
invalid local Compose endpoint maps. It also proves source discovery emits a
stable, bounded path set containing every fixed image input, the nested Ollama
context, and the complete in-module `cmd/segmentor` dependency closure for the
actual Linux `localocr` build constraints. `make ocr-build-tags` depends on
that focused test, then runs the Kraken installer behavior plus the default,
`remoteocr`, and `localocr` Go build combinations. The installer proves that a
matching model digest is accepted and a tampered artifact is rejected before
the file can be copied into the runtime image.

`make toolchain-check` keeps `.go-version`, `.nvmrc`, `.tool-versions`, Docker
bases, test images, and workflow Terraform versions aligned. Its bootstrap
path uses standard shell tools, and behavior tests install the checksum-pinned
helpers with `make install-shell-tools`. `make
frontend-image-smoke` starts the packaged frontend read-only, overrides only
the PPB edge mode so the undeployed image does not require a live backend, and
fetches its static application. This resolves the runtime module graph before
Terraform can deploy an image with an omitted `COPY` input; PPB forwarding and
canonical-origin behavior remain covered by the frontend server tests. `make
readiness-fixture-test`,
`make deployment-status-test`, `make preview-deployment-test`, and `make
verify-cloud-backups-test` exercise deployment contracts without cloud access.
Protected previews deliberately execute cloud orchestration from `main` while
testing the pull-request image. The frontend readiness probe therefore supports
one mixed orchestrator/image rollout: a legacy orchestrator may omit its
expected-origin input, but the backend must still report an exact HTTPS origin;
once the input is present, the report must exactly match Terraform's
deterministic origin.
Deployment-status precedence and preview event/dispatch resolution are typed Go
unit tests under `internal/deployer`; their shell entrypoints contain no policy
logic. The preview tests prove the protected-main checkout, fork handling,
retargeted teardown, recovery-mode selection, and validated output projection.
Cloud Run execution fencing, launch recovery, terminal settlement, and bounded
diagnostics are typed Go tests under `internal/cloudrunreadiness` and
`cmd/cloud-run-readiness`. The `ci/run-cloud-run-readiness.sh` entrypoint is a
thin binary launcher and contains no lifecycle state machine or readiness
policy. Protected-preview backend readiness may start one additional owned
execution, while the exact production backend job may start up to five
additional owned executions. Every failed nonfinal execution must independently
contain the exact allowlisted startup-gate, frontend-transport, and VM-network
marker set proving guest-startup lag. All attempts use distinct execution
markers and share one 45-minute execution deadline; OCR, browser, cancellation,
control-plane, contract, and unavailable-marker failures are never retried.
Production browser job rebinding, exact secret-version reconciliation, IAP
transfer, restricted remote session minting, and cleanup settlement are typed
Go tests under `internal/productionbrowserreadiness` and
`cmd/production-browser-readiness`. The
`ci/run-production-browser-readiness.sh` entrypoint is also a thin launcher;
presentation-formatted or tabular gcloud output and Bash lifecycle state are
forbidden at this boundary. Secret inventory comes from a bounded Secret
Manager API projection; the remaining gcloud metadata uses no-transform JSON.
The typed client validates each response before acting. Its bounded settlement
poll may retry an unavailable list or a structurally valid but incomplete
observation; malformed, mis-scoped, duplicate, unknown-state, or over-limit
records fail immediately. Exhaustion reports only a fixed inventory,
placeholder, or version-set category. The same Go tests prove that the VM
executable and lock use the executable production filesystem while the
same-suffix credential directory remains under private `/tmp`, every Compose
call uses the fixed managed Docker configuration, prepare performs bounded
recovery of both current and former `/tmp` stage layouts, and absent credential
state is a valid cleanup retry. They also assert that prepare, mint, and cleanup
controller deadlines strictly contain their VM command budgets, cleanup
success reaches the controller before inert-stage finalization, and the outer
cleanup deadline contains the combined bounded typed-cleanup, residual
finalization, and inert job-binding restoration retry budgets. A
cleanup-precedence test requires the final
`cleanup-unconfirmed` marker and the separate fixed primary category while
rejecting request identifiers and private paths from diagnostics.
The preview test proves teardown consumes the workspace's recorded immutable
inputs, never calls a registry or build tool, and rejects missing or corrupt
state without printing its contents. It also proves the explicit protected
recovery path reads only the newest valid lower-serial, same-lineage input from
versioned state history and never restores or pushes a historical state file.
When a prior destroy already removed the exact workspace, the recovery fixture
requires a successful authoritative inventory before accepting that state as
complete; ordinary destroy, failed inventory, and failed selection of a listed
workspace remain fail closed. The operations contracts exercise bounded,
redacted retries for root Vault `GET ?list=true`, leaf `DELETE`, and curl transport
failures, immediate failure for non-transient HTTP responses, and preview status
gating on successful Vault namespace cleanup.

Managed readiness exercises the exact deployed frontend/API path plus private
image normalization, the default Scribe segmentation, Kraken transcription,
and the production default Ollama OCR request. Fixture contracts require authenticated
requests, real image bytes, JPEG validation, non-empty model output, and Ollama
`done=true`; a health-only response is not deployment evidence.

Managed preview and production readiness also run a digest-pinned Playwright
image in the environment's root-owned application VPC, avoiding an additional
network quota slot. Reviewed state moves transfer the existing VPC and
application subnet from nested-module ownership to the root without resource
replacement, and Cloud Compose consumes their exact self-links. The browser
job uses a dedicated, non-overlapping dual-stack `/26`; its interface tag
selects an egress firewall deny for the exact private application subnet CIDR.
The browser subnet receives one external IPv6 `/64`, and only that
environment-owned `/64` is added to the same environment's PPB policy. Its
reserved IPv4 address and subnet-scoped Cloud NAT remain available only for
fixed public DNS and reviewed IPv4-only fixture origins; canonical `run.app`
traffic is forced over AAAA because Public Cloud NAT does not translate Google
service traffic. A protected, unit-tested helper bounds and validates
public-global AAAA answers, supplies Chromium's exact-host mapping, and
disables Node IPv4 racing for Playwright API requests. The singleton foundation
binds Google's required `roles/compute.publicIpAdmin` only to the managed Cloud
Run service agent, never to preview or production workspace state, deploy
identities, or application identities. Immediately after browser-context
creation, a bounded initial-root warm-up may retry only PPB `403` or `404`
responses for five minutes before production authentication, cleanup, and
strict browser monitoring. Preview trusted orchestration fetches only the
readiness script at the exact resolved same-repository PR-head SHA before cloud
authentication, walks its exact commit tree, requires unique tree parents and a
`100644` source blob, and reconciles the bounded Contents payload with that blob
before substituting only that file into a protected-base Docker build. Symlinks,
gitlinks, duplicate or truncated tree results, and mismatched payloads fail
closed. The protected Dockerfile and dependencies never execute the script
during the credentialed build. Its no-IAM preview service account later runs
the script with preview-anonymous auth to exercise the PR-head frontend's
complete upload-to-editor handoff with Scribe segmentation and deterministic
Tesseract line transcription, canonical annotations, overlay on/off semantics,
resolver-backed default-context selection and the resulting concrete context.
The retry-bounded upload uses a reviewed two-line fixture and delays the editor
bundle until durable transcription completes. The editor must reconcile that
exact job and canonical revision, visibly move its catch-up wand between the
two lines, and emit matched per-line events. It then mounts the editor before a
distinct durable job starts, waits for both the item-scoped SSE handshake and
the correlated ready-after-reconciliation application marker, then proves live,
attempt-scoped event and wand progress through both lines after the exact new
job becomes durably terminal plus a bounded UI-drain grace. Both paths must make
no automatic foreground
`EnrichAnnotation` calls and wait for an exact canonical-reload acknowledgment
before unblocking retranscription. Each intercepted upload request and uncertain
late-commit cleanup retain a 300-second horizon; the whole retry-to-editor wait
uses the remaining 27-minute main-scenario budget. Behavioral tests cover that
budget relationship, both fixed upload classifiers, and exact marker parsing.

Production builds the same runner exclusively from the exact reviewed `main`
tree and does not depend on a user's cookie or Google OAuth. The protected
deploy identity can open an IAP tunnel only to port 22 on the exact production
VM and can manage versions only on the exact browser-session secret. It mints a
fixed 50-minute session for reserved user/workspace 1, validates the bounded
mode-`0600` storage state, and uploads one temporary Secret Manager version.
The isolated Cloud Run identity can only read that secret. Its entrypoint
unsets the injected value before Node starts; the runner removes the transient
file, verifies the version-bound digest and cookie contract in memory, and
checks the exact non-system user, workspace, and administrator role before any
product action. Browser executions reach natural terminal state so final
cleanup can retry fail-closed logout independently of Chromium and prove the
original cookie receives HTTP 401 from a protected API. Only then may transport
restore the exact inert version-1 job reference and destroy and verify the known
numeric credential version without relying on `latest` or list consistency.
Ambiguous credential creation remains a failed
deployment with bounded best-effort reconciliation and fixed expiry. A
pre-mutation production janitor deletes only exact readiness-owned UUID
resources and manifest items carrying both the strict readiness marker and
reviewed source URL. A subsequent protected apply fences work left by hard
termination before rollout; the
50-minute lifetime is the final revocation fallback.

The scenario then exercises retranscription, live word/line transforms, save,
publish, destructive-action presentation, the copy-once token modal, and exact
six-Canvas manifest import. Responsive coverage loads the saved editor once,
resizes it in place through 360x800, 667x375, 768x1024, and 1440x900, and
requires all 14 primary actions without scrolling, a usable OpenSeadragon image
area, and unchanged canonical state. The manifest gate
requires a mounted Scribe action panel, bounded nonempty OpenSeadragon image
evidence, exact active Canvas/item-image/URL agreement, canonical and public
Presentation AnnotationPages, a real Mirador page turn, and a usable editor
after cycling all overlay modes on the second Canvas. The runner deletes the
upload through the homepage and the manifest item through the sidebar, accepts
only exact item-ID confirmation dialogs, and requires both rendered library
copies to disappear. Cleanup then directly reconciles exact upload, manifest,
and token identities through the bounded late-commit horizon. Same-origin
4xx/5xx responses outside the allowed upload retry sequence, request failures,
CSP console errors, unexpected native dialogs, missing annotations, and leaked
cleanup state are failures. Cloud diagnostics admit only exact stage
categories, including `structure`, `manifest`, and `rate`, plus fixed
endpoint-family and client/server/transport variants for generic network
failures. A `rate` marker is paired only with its fixed endpoint family, not a
path, request, response, or credential. A top-level `upload` marker may be paired only with separately
allowlisted fixed substage and durable-failure markers. A final retryable image
response also emits only a fixed marker for its exact canonical lowercase
Connect code or, when the capped response snapshot is unavailable or invalid,
its fixed observed HTTP status. A valid JSON snapshot with a missing,
non-string, or unrecognized code fails closed; the status-only marker does not
attribute which component produced the response. The durable classifier
reduces terminal status through exact safe provider/segmentation messages or
the fixed `admission`, `upload-storage`, `segmentation-output`, `quota-resize`,
`lease-renewal`, `image-commit`, `ocr-run-commit`, `annotation-commit`,
`transcription-enqueue`, `item-reload`, and `batch-commit` stages, and rejects
fixture names and free-form detail. Lease renewal recurs before fenced side
effects and overrides the interrupted operation. The exit-code mapping
preserves those bounded variants when Cloud Logging queries are unavailable,
and cleanup cannot overwrite the original browser fault. Free-form messages,
raw browser output, request URLs, and credential-bearing state are neither
generated nor uploaded.

The Go tests under `internal/cloudrunreadiness`, `cmd/cloud-run-readiness`,
`internal/productionbrowserreadiness`, `cmd/production-browser-readiness`,
`internal/deployauth`, and `cmd/browser-session` are authoritative for
readiness, credential, remote-session, cleanup, signal, and diagnostic
lifecycle transitions. The executable launcher tests
`ci/run-cloud-run-readiness_test.sh` and
`ci/run-production-browser-readiness_test.sh` retain only observable argv,
stream, rejection, and exit behavior. The protected hosted deployment and its
Terraform plans/applies provide the end-to-end evidence for reviewed source,
image digests, network isolation, IAM, and the managed 27-minute browser
scenario; source-text snapshots are not treated as evidence for those effects.
`ci/readiness-fixture-test.sh` proves the runner's embedded bytes and declared
SHA-256 match the committed deterministic opaque PNG. The reusable deploy
workflow gives preview 120 minutes and production 240 minutes for browser
fencing, forward readiness, and rollback recovery.

`make generate` consumes the reviewed dependency commits in `proto/buf.lock`.
To upgrade a Buf module deliberately, run `cd proto && ../.tools/bin/buf dep
update .`, review the lock diff, then regenerate; CI never floats that lock on
its own.
