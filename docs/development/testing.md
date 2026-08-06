# Testing

Use the smallest test that proves the invariant, then cover cross-boundary
behavior where data can drift.

```bash
make check              # fast lint/generated/unit pre-push loop
make test-backend-fast  # cached, parallel Go unit/race tests without MariaDB
make test-backend       # Go unit/race and DB tests
make test-frontend      # web + plugin tests and production builds
make test-browser       # real Chromium editor/session/geometry acceptance
make e2e-smoke          # focused DB-backed ingest/revision subset
make backup-restore-smoke # isolated DB/blob restore + job recovery
make ocr-matrix-test    # OCR catalog, route, and baked-artifact invariants
make ocr-build-tags     # default, remoteocr, and localocr builds
make export-schema-check # PAGE 2019-07-15 + ALTO 4.4 XSD validation
make ci                 # complete local CI contract
```

`ci/run-ci.sh` defines the hosted/local group boundary once. Run a focused
group directly (`./ci/run-ci.sh contracts`, `test`, `browser`, `recovery`,
`security`, or `infrastructure`) when reproducing one required GitHub job;
`make ci` runs the complete set.

For focused deployment-orchestration iteration, run `go test
./internal/deployer ./cmd/deployer`. The corresponding workflow shell files are
thin launchers; preview selection and deployment-status precedence belong in
these Go tests.

`make test-backend-fast` is the shortest backend loop. It unsets `TEST_DSN`,
uses the exact host Go toolchain plus C race-detector compiler when available,
runs packages in parallel, and retains Go's test-result cache so source or
dependency changes rerun the affected packages. If that host toolchain is not
available, it uses the same prepared container as the full backend gate. This
fast target omits export-schema validation and DB-backed acceptance tests.

Containerized Go gates keep module and build caches in named Docker volumes.
The Dockerfile's `test-runner` stage pins `build-base` and `xmllint`, so repeated
test invocations reuse a prepared image instead of installing Alpine packages
on every run. The test source and MariaDB data remain isolated: `make ci` still
creates and removes a unique database project on every run, while Go validates
cached modules and build artifacts against `go.sum` and source build IDs. Go's
build cache can also hold successful test results, so the full backend and
smoke harnesses use `-count=1` to execute every acceptance test against the
current database.
DB-backed Go packages share that one suite-local schema and run with `-p=1`
because they intentionally exercise schema-global quota and outbox
materializations. This serializes package test binaries; it does not replace
the unique Compose project that isolates the suite. Explicit goroutine and
race tests inside each package still run concurrently.

Required local and hosted CI set `SCRIBE_REQUIRE_TEST_DB=true`, so the backend
gate fails if it cannot attach to the isolated MariaDB service. The focused
`make e2e-smoke` target remains available for fast ingest/revision iteration;
the required gate does not rerun that subset after `go test ./...` has already
executed the same DB-backed tests.

`make test-backend` runs the export schema check before Go tests. With an exact
host Go toolchain it can run locally when `xmllint` and `sha256sum` are also
installed; otherwise it selects the prepared test-runner image automatically.
Schema validation is offline: official PAGE, ALTO, and XLink schemas are
vendored with checksums under
`internal/server/testdata/crosswalk/schemas/`.

Release-blocking integration scenarios include:

- two workspaces importing the same Canvas without annotation leakage;
- line-to-word save/reload and export;
- line and word edits remaining identical through public dereference plus
  hOCR, PAGE XML, ALTO XML, and plain-text exports;
- expected-revision conflict between two editors;
- a dirty draft receiving background transcription;
- batch ingest retry/resume without duplicate images;
- image-only and hOCR-backed Presentation 3/Choice manifest dereference,
  crash/reclaim idempotency, and `iiif-spec` validation;
- authenticated background-worker tenant scope, provider/workspace admission,
  atomic OCR provenance quota failure, and concurrent last-admin protection;
- provider error redaction and endpoint allowlisting.

Tests that only assert an event was emitted are not sufficient when production
behavior depends on a listener or persistence side effect.

## Browser acceptance boundary

The Playwright suite under `web/e2e/` runs only through the pinned container in
`ci/test-browser.sh`; host browser installations are neither required nor used.
Its harness imports production modules rather than copying editor behavior:

- `editor/leave-dialog.ts`, used by `renderEditor`, owns the dirty-editor
  dialog and keyboard focus loop;
- `editor/geometry.js` runs against a real OpenSeadragon viewer at nonzero page
  offset, scroll, pan, and zoom;
- `editor/session.js` owns dirty-state and three-way background rebasing;
- `ScribeAnnotationAdapter` performs save, reload, and stale-revision handling
  through the generated Connect client and the real MariaDB-backed canonical
  page repository when the test database is running.

`make ci` requires that database-backed boundary; the browser gate fails rather
than silently substituting a fake. A deterministic in-browser CAS implementation
remains available for fast standalone `make test-browser` runs when MariaDB is
not running. Authentication policy, a full live Mirador workspace, and worker
delivery remain covered by the required Go integration suite; `make e2e-smoke`
selects the core ingest/revision cases for focused iteration.

The managed preview scenario is separate from the local harness. Trusted
`pull_request_target` orchestration checks out the protected base, then before
cloud authentication retrieves exactly `web/e2e/deployed-readiness.mjs` at the
resolved same-repository PR-head SHA. The bounded helper resolves that exact
commit, walks the non-recursive Git trees for `web/` and `e2e/`, requires both
parents to be trees and the leaf to be one `100644` blob, then reconciles its
SHA and size with the Contents API payload before replacing only that path.
Symlinks, gitlinks, duplicate entries, truncated trees, and mismatched payloads
fail closed. The protected Dockerfile, package manifests, and dependencies
remain from the base; its credentialed build copies but does not execute the PR
script. The script runs only after the preview apply in a no-IAM, preview-only
Playwright job. The scenario uses a browser-only `/26` and NAT to target the canonical
`scribe-pr-*` Cloud Run origin, uses preview-anonymous auth and
the built-in Tesseract context, and deletes the workspace token and uploaded
item it creates. It verifies the completed private draft through
`AnnotationService.GetAnnotationPage`, then requires its public Triplet
AnnotationPage only after the editor publishes that revision. It deliberately
produces no browser artifacts; on failure the
Cloud Run helper retains only an exact allowlisted stage category, including
the bounded `structure` and `manifest` categories; free-form messages are
discarded. Run `bash
ci/browser-readiness-contract_test.sh` and `bash
ci/run-cloud-run-readiness_test.sh` for focused orchestration iteration. A
feature PR can change only the deployed readiness script in this trusted build
path, while protected-base orchestration and dependencies package it to
exercise the PR-head frontend and backend images.
