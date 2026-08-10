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
script. The reviewed two-line upload fixture and its SHA-256 digest are embedded
in that attested script, so the build does not combine it with a separately
staged PR-head fixture. The source SHA tags the image and Terraform records its
resolved digest. The script runs only after the preview apply in a no-IAM,
preview-only Playwright job with digest-pinned runtime and browser dependencies.
Its dedicated service account has no project, storage, Vault, Pub/Sub, or OCR
IAM grants. Each preview reuses its root-owned application VPC instead of
consuming a second project-wide network quota slot. Cloud Compose consumes that
VPC and its application subnet as existing resources; reviewed `moved` blocks
transfer their Terraform ownership from the nested module to the root without
recreating either object. The browser job attaches to a dedicated,
non-overlapping dual-stack `/26` in that VPC. Its network-interface tag selects
an egress firewall deny for the exact application subnet CIDR, so the untrusted
preview runner cannot use the common preview VPC to reach private application
addresses. The browser subnet receives one external IPv6 `/64`; that
preview-owned `/64` is the sole browser range added to the preview's PPB policy,
so production and other previews are not widened. The singleton project
foundation grants
`roles/compute.publicIpAdmin` only to Google's Cloud Run service agent, as
required for external-IPv6 Direct VPC addresses; no preview workspace, human,
deploy, or application identity owns that project-wide grant. A subnet-scoped
Cloud NAT and reserved regional IPv4 address exist only for fixed public DNS
and reviewed IPv4-only fixture origins. The exact-head runner must force the
canonical `scribe-pr-*` host over AAAA and fail closed if it cannot, because
Public Cloud NAT does not translate `run.app` traffic. A protected helper
retries fixed public AAAA resolution for at most two minutes, accepts at most
32 public-global answers, selects one deterministic address for Chromium's
exact-host resolver rule, and disables Node's IPv4 family race for Playwright
API requests. Direct VPC plus Cloud NAT does not destination-filter arbitrary
public IPv4 browser egress, so the additional boundaries are the runner's exact
configured HTTPS origin, host-only session cookie, no-data identity, and
bounded product script.

The scenario uses preview-anonymous auth and leaves the upload selector on
resolver-backed `Default` (`0`), then verifies the resulting durable
transcription job records a concrete nonzero context. It uploads a reviewed
two-line fixture and intentionally delays the editor bundle until that exact
job completes. The editor must reconcile the completed job and canonical
revision, show the catch-up magic wand moving through both line annotations in
order, and emit matched start/result events for the exact successful attempt.
The runner then mounts an unpinned editor before enqueueing a distinct durable
job, waits for that editor's item-scoped SSE response so the server has captured
its outbox high-water mark, and requires live, non-catch-up start/result events
plus the visible wand's line-by-line movement. Neither automatic path may make
a foreground `EnrichAnnotation` request. The editor must keep retranscription blocked
until Mirador acknowledges the exact correlated canonical reload. Retryable
image-upload responses are evaluated as one frontend retry sequence: readiness
requires one to three attempts containing only retryable predecessors and an
exact final success before editor handoff.

The scenario verifies the completed private draft through
`AnnotationService.GetAnnotationPage`; exercises overlay modes, retranscription,
draw mode, centered creation, undo/redo, inline text editing, word and line
split/join transforms, editor deletion, save, and publish; and then requires
the public Triplet AnnotationPage. The saved editor is loaded once and resized
in place through 360x800, 667x375, 768x1024, and 1440x900 so the mounted bottom
pane must follow every viewport change. Every viewport must expose all 14
primary actions without scrolling, retain a usable OpenSeadragon image area,
and leave the canonical revision and page byte-for-byte unchanged. It also
imports the exact Lehigh six-Canvas manifest in its default preserve-hOCR mode.
The imported-item path requires the
Scribe panel, an expected nonempty OpenSeadragon image response, and exact
active Canvas, item-image, and editor-route identity. It validates the first
page through the private canonical API and the exact public Presentation
AnnotationPage, uses Mirador's real **Next item** control, and repeats those
checks on the second Canvas before cycling every overlay mode and confirming an
editor action remains enabled. Publication is limited to the disposable local
item and exact cleanup removes the import. Both home-library copies of each
disposable item must expose the exact final destructive trash action. The runner
deletes the upload through the homepage and the manifest item through the
sidebar, accepts only the exact item-ID confirmation dialog, and requires both
rendered copies to disappear after each deletion. Finally, it deletes the
copy-once workspace token and directly reconciles the upload by exact name and
the manifest item by exact source URL. The scenario records the latest upload,
manifest-import, and token-creation request time. If the corresponding response
did not settle and validate, direct reconciliation continues deleting exact
matches through the full 300-second request commit horizon and then requires
stable absence. This prevents a lost response or a canceled browser request
from committing a resource after cleanup. Manifest reconciliation scans the
capped workspace inventory with an empty query before loading each candidate
and matching the exact source tuple; token cleanup lists keys and matches only
the unique generated name, without reading a secret.

The scenario allows 300 seconds for upload handoff so the frontend's 270-second
upstream cap can cover the backend's 240-second scale-to-zero inference budget.
The proxy charges backend wake time and upstream work to one 285-second request
budget below the platform cutoff. The complete product scenario has a 30-minute
deadline measured from runner startup. Reaching it closes the page so the normal
failure path can use the retained API session, rather than letting the platform
terminate browser work mid-mutation. The runner reserves the final 10 minutes
of its 40-minute Cloud Run task for deadline-aware cleanup. That reserve covers
the 300-second commit horizon, a 180-second recovery tail, and bounded
request/control overhead.

The protected deploy job keeps its existing 120-minute ceiling. Its executable
contract reserves 300 seconds for backend readiness, 1,800 seconds for OCR,
2,400 seconds for the browser task, and 1,800 seconds for build, Terraform, and
control-plane work. The scenario produces no browser artifacts; on failure the
Cloud Run helper retains only an exact allowlisted stage category, including
the bounded `structure` and `manifest` categories; free-form messages are
discarded. Run `bash
ci/browser-readiness-contract_test.sh` and `bash
ci/run-cloud-run-readiness_test.sh` for focused orchestration iteration. A
feature PR can change only the deployed readiness script in this trusted build
path, while protected-base orchestration and dependencies package it to
exercise the PR-head frontend and backend images.
