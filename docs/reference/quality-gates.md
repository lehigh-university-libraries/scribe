# Quality gates

Required pull-request checks cover:

- gofmt, ShellCheck, actionlint, golangci-lint, and Buf lint;
- generated Go, TypeScript, sqlc, and OpenAPI drift;
- Go race tests, DB integration tests, release-target 32-bit cross-compilation,
  and web/plugin tests and builds;
- real Chromium editor acceptance tests in a pinned Playwright container;
- packaged Scyllaridae PDF conversion with corrected Unicode text and exact
  image/page-order checks through Poppler;
- OCR build tags and DB-backed ingest/revision acceptance tests;
- isolated backup/restore integrity and expired-job recovery smoke tests;
- gosec, npm audits, and Trivy dependency
  plus credential scanning with a synthetic detection regression;
- hash-locked segmentor Python transitives, repository dependency and secret
  scanning, digest-pinned runtime images, and packaged-runtime smoke tests;
- Terraform formatting/init/validation, rendered Compose checks, and the local
  runtime, Vault-init, and secret-generation script tests;
- Zensical documentation build.

`make ci` is the local entrypoint and includes the same Trivy high/critical
dependency and secret scan as the hosted workflow. Runtime image scanning is
currently deferred and does not gate CI, deployment, or release. Individual
component commands remain useful for iteration. Reachable Go vulnerability
analysis is optional during development: run
`SCRIBE_GOVULNCHECK=true make security` to include the pinned `govulncheck`.
It is not a `make ci` or hosted CI gate. A manually checked box is
not a substitute for a passing required job. `ci/run-ci.sh` owns the canonical
`contracts`, `test`, `browser`, `recovery`, `security`, and `infrastructure`
groups; hosted jobs call those same groups in parallel while `make ci` runs
them locally. The orchestrator creates a unique Compose project from the
reviewed base file (never a developer's local override), lets Docker allocate a
collision-free bridge, waits for MariaDB health before integration tests, and
removes its containers and volumes on success, failure, or interruption. It
does not reuse or stop the normal development stack, so integration tests
cannot silently skip on a clean checkout.

Each npm advisory request has a 60-second transport timeout and at most three
attempts. A nonzero result on every attempt still fails the security gate, so a
registry outage is bounded without allowing a real advisory finding to pass.

`make ocr-build-tags` cross-compiles both GoReleaser binaries for `linux/386`
with the release build tag in the same pinned Go container used for its native
build checks. Hosted CI and the local entrypoint therefore reject constants and
other code that compile on 64-bit development hosts but fail a supported
release target.

`make segmentor-lock-check` proves every Python requirement is exact and every
accepted distribution has a SHA-256 hash, including the explicitly retained
unsafe `setuptools` transitive. `make segmentor-lock` regenerates that file in
the digest-pinned Python image with an exact `pip-tools` version. Release images
install it with both `--require-hashes` and `--only-binary=:all:`.

The code-generation, documentation, and security entrypoints also inspect host
tool versions before execution: Buf 1.72.0, sqlc v1.31.1, Zensical 0.0.65,
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

`go test ./internal/ocrimages` checks the OCR image matrix built from
`config/ocr.yaml`: every service is emitted with the right baked model, service
names match Terraform's, and invalid catalogs (missing artifacts, unknown
defaults, colliding default filenames, mutable bases, reserved IDs) are
rejected. `make ocr-build-tags` runs the Kraken installer behavior plus the
default, `remoteocr`, and `localocr` Go build combinations. The installer proves
that a matching model digest is accepted and a tampered artifact is rejected
before the file can be copied into the runtime image.

`make toolchain-check` keeps `.go-version`, `.nvmrc`, `.tool-versions`, Docker
bases, test images, and workflow Terraform versions aligned. `make
frontend-image-smoke` starts the packaged frontend read-only and fetches its
static application, so an omitted `COPY` input fails before deployment. `make
readiness-fixture-test` proves the OCR readiness probe's embedded image matches
the committed deterministic PNG.

## Deployment checks

Every apply executes finite Scribe and Triplet schema jobs before creating new
Cloud Run service revisions. The backend readiness job checks API and worker
HTTPS readiness, the immutable deployed API image, and canonical origin. The
OCR readiness job sends a real image through the private registered model
endpoints. Failed migrations or readiness executions fail deployment.

`make generate` consumes the reviewed dependency commits in `proto/buf.lock`.
To upgrade a Buf module deliberately, run `cd proto && ../.tools/bin/buf dep
update .`, review the lock diff, then regenerate; CI never floats that lock on
its own.

Managed database acceptance is `make test-mysql`. It runs the full Go suite
against digest-pinned MySQL 8.4 with the deployed Unicode collation, alongside
the local MariaDB contract. Recovery smoke uses the same MySQL family and
verifies migration-ledger, canonical/publication, and expired-lease recovery.
