# Testing

Use the smallest test that proves the invariant, then cover cross-boundary
behavior where data can drift.

```bash
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

Containerized Go gates keep module, build, and golangci-lint caches in named
Docker volumes so repeated local runs do not redownload or recompile the entire
dependency graph. The test source and MariaDB data remain isolated: `make ci`
still creates and removes a unique database project on every run, while Go
validates cached modules and build artifacts against `go.sum` and source build
IDs. Go's build cache can also hold successful test results, so the backend and
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

`make test-backend` installs the pinned `libxml2-utils=2.13.9-r2` package in
the immutable Go test container and runs the export schema check before Go
tests. The gate is offline: official PAGE, ALTO, and XLink schemas are vendored
with checksums under `internal/server/testdata/crosswalk/schemas/`. Running the
focused target directly requires `xmllint` plus `sha256sum`; the script reports
installation guidance when either tool is missing.

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
