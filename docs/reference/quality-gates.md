# Quality gates

Required pull-request checks cover:

- gofmt, ShellCheck, actionlint, golangci-lint, and Buf lint;
- generated Go, TypeScript, sqlc, and OpenAPI drift;
- Go race tests, DB integration tests, web/plugin tests and builds;
- real Chromium editor acceptance tests in a pinned Playwright container;
- OCR build tags and DB-backed ingest/revision smoke tests;
- isolated backup/restore integrity and expired-job recovery smoke tests;
- gosec, reachable Go vulnerability analysis, npm audits, and Trivy dependency
  plus credential scanning with a synthetic detection regression;
- hash-locked segmentor Python transitives, repository dependency and secret
  scanning, digest-pinned runtime images, and packaged-runtime smoke tests;
- Terraform formatting/init/validation and operational identity, action-pin,
  runtime-limit, immutable rollback/drift replay, rollback-status,
  readiness-fixture, backup-policy, and Container-Optimized OS host-jq
  portability contracts;
- executable WIF fixtures that reject broader repository, workflow, ref,
  environment, issuer, mapping, pool, or service-account bindings, followed by
  the same fail-closed inspection against live GCP in every protected workflow;
- release-chain fixtures that permit only the typed, exact-current-main release
  dispatch and reject arbitrary-ref dispatch or pushed-tag credential access,
  PR-head release checkouts, and legacy tag workflows, plus a fake Git/GitHub
  retry proving a later main HEAD is rejected, the requested numeric version
  is exact, a partial publish reuses the unmoved tag and draft, downloaded
  Linux/Darwin/Windows assets match their checksums, a completed release is a
  no-op, and an out-of-order older merge skips only after a later exact release
  is complete;
- a fake-Docker destructive-operation contract proving the local database reset
  refuses missing confirmation, CI, mislabeled resources, and broad volume removal;
- Zensical documentation build.

`make ci` is the local entrypoint and includes the same Trivy high/critical
dependency and secret scan as the hosted workflow. Runtime image scanning is
currently deferred and does not gate CI, deployment, or release. Individual
component commands remain useful for iteration, but a manually checked box is
not a substitute for a passing required job. The orchestrator creates a unique
Compose project, waits
for its MariaDB health check before backend tests, and removes its containers and
volumes on success, failure, or interruption. It does not reuse or stop the
normal development stack, so integration tests cannot silently skip on a clean
checkout.

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

Because trusted `pull_request_target` orchestration is associated with the
protected base revision, the preview workflow records separate deployment
evidence against the exact tested PR head. That no-checkout job revalidates the
live PR, disables auto-merge, marks the deployment transient and non-production,
and can report `success` only after the managed revision and public readiness
checks pass. Failed reruns supersede same-PR successes with terminal evidence;
stale runs cannot approve a newer head, and successful teardown marks that PR's
records inactive. `make preview-deployment-test` statically enforces this trust
boundary and the immutable teardown contract.

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

`make ocr-matrix-test` validates the OCR catalog and its deploy/runtime/local
alignment without starting a build container. It rejects missing artifacts,
unknown defaults, route drift, model-file collisions, and disagreement between
`config/ocr.yaml`, the segmentor Dockerfile defaults, and local Compose endpoint
maps. `make ocr-build-tags` depends on that cheap contract, then runs the Kraken
installer contract plus the default,
`remoteocr`, and `localocr` Go build combinations. The installer proves that a
matching model digest is accepted and a tampered artifact is rejected before
the file can be copied into the runtime image.

`make toolchain-check` keeps `.go-version`, `.nvmrc`, `.tool-versions`, Docker
bases, test images, and workflow Terraform versions aligned. Its bootstrap
path uses standard shell tools; contracts that use ripgrep first install the
checksum-pinned release with `make install-shell-tools`. `make
frontend-image-smoke` starts the packaged frontend read-only and fetches its
static application, resolving the runtime module graph before Terraform can
deploy an image with an omitted `COPY` input. `make readiness-fixture-test`,
`make deployment-status-test`, `make preview-deployment-test`, and `make
verify-cloud-backups-test` exercise deployment contracts without cloud access.
The preview test proves teardown consumes the workspace's recorded immutable
inputs, never calls a registry or build tool, and rejects missing or corrupt
state without printing its contents.

Managed readiness exercises the exact deployed frontend/API path plus private
image normalization, segmentation, Kraken transcription, and the production
default Ollama OCR request. Fixture contracts require authenticated requests,
real image bytes, JPEG validation, non-empty model output, and Ollama
`done=true`; a health-only response is not deployment evidence.

`make generate` consumes the reviewed dependency commits in `proto/buf.lock`.
To upgrade a Buf module deliberately, run `cd proto && ../.tools/bin/buf dep
update .`, review the lock diff, then regenerate; CI never floats that lock on
its own.
