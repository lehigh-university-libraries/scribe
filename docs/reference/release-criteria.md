# Release criteria

Scribe uses evidence-based release approval. A capability is complete only when
its implementation, automated acceptance test, generated contracts, and
relevant documentation are committed and the required GitHub jobs pass. UI
text, emitted browser events, unit mocks, or a manually checked box are not
sufficient evidence by themselves.

## Current status

Every push to `main` deploys to production once CI passes, and each merged pull
request gets a release tag. Editing this page or checking a box is never
release evidence.

The codebase is greenfield. Backward compatibility and data migrations are not
constraints for the current hardening pass; prefer a clean invariant over a
compatibility layer and do not preserve duplicate persistence or API paths.

Run the complete repository contract with:

```bash
make ci
```

The required GitHub jobs independently verify contracts/lint, tests, security,
and infrastructure/documentation. See [quality gates](quality-gates.md) for the
individual checks.

## Release blockers and executable evidence

Every capability below has committed automated acceptance evidence. The names
are deliberately searchable so a contributor or agent can find the owning test
without copying a second test inventory into `AGENTS.md`.

| Required capability | Primary executable evidence |
| --- | --- |
| Same-manifest tenant isolation across read, mutation, search, export, and publication | `TestSameManifestIsIsolatedAcrossWorkspaces` |
| One canonical revision source for editor, worker, manifests, exports, metrics, and publication | `TestCanonicalHOCRAndExportDoNotFallbackToOCRRun`, `TestSaveAnnotationPageCommitsCorrectionMetricWithCanonicalRevision`, `TestPublishedPresentationGraphUsesCanonicalSnapshotsAndTripletIDs` |
| Atomic `GetAnnotationPage` / `SaveAnnotationPage(expected_revision)` semantics and explicit conflicts | `TestAnnotationPageRevisionSaveSemantics`, `TestAnnotationAdmissionIsAtomicAndBulkIndexIsChunked` |
| Word CRUD and split/join survive save, reload, public HTTP dereference, and every export | `TestWordStructuralLifecyclePersistsAcrossCanonicalPublicAndExports` |
| Background transcription rebases a dirty mounted editor without losing local state | Browser test `a production SSE completion rebases the mounted dirty editor through Connect` |
| Context-bound multi-file ingest progress, idempotency, retry, resume, partial failure, and cancellation | `TestUploadBatchConnectAcceptanceResumeIdempotencyAndCancellation`, `TestUploadBatchDurableResumeRetryAndCancellationFence` |
| Immutable lease/input-revision-fenced attempts cannot overwrite newer corrections | `TestTranscriptionAttemptsFenceExpiredWorkersAndCompleteAtomically`, `TestTranscriptionAttemptCannotOverwriteNewerHumanCorrection` |
| IIIF v3 array context and image `Choice` import plus `libops/iiif-spec`-valid output | `TestPresentation3ArrayContextChoiceImportAndLibopsEmission` |
| Administrator-owned provider origins/audiences and credential/response redaction | `TestProviderConfigUsesExactServerOwnedModelRoute`, `TestOllamaAudienceMustMatchRegisteredEndpointOrigin`, `TestProviderRedactionAcrossRegisteredAdapterLogsAndListedAudit` |
| Real-browser focus, keyboard, geometry, zoom, rebase, save/reload, and conflict behavior | `web/e2e/editor.browser.ts` |
| The minimum raw editor deep link opens its exact item image without prior Scribe navigation | Browser test `a raw editor deep link opens the requested item without prior navigation` in `web/e2e/editor.browser.ts`; workspace query propagation in `web/src/lib/workspace.test.ts` |
| Managed SQL persistence, multi-replica publication CAS, and opaque secret copy | `make test-mysql`, `TestTripletSQLReplicas`, `TestCredentialLifecycleAndDeploymentIsolation`, `cmd/secret-manager-secrets/main_test.go` |
| Isolated backup/restore and job recovery | `make backup-restore-smoke` and the `recovery-smoke` job |

Every job used by `make ci`, including `recovery-smoke`, must pass. GitHub jobs
invoke the same Make targets and repository scripts as the local contract.

## Definition of done for a change

- [ ] Source and generated files are formatted and current.
- [ ] The smallest meaningful unit or contract tests pass.
- [ ] Cross-boundary behavior has an integration or browser test where drift or
  concurrency is possible.
- [ ] Authorization, tenant isolation, input cost, secret/log exposure,
  idempotency, and failure recovery were considered.
- [ ] API, configuration, and architecture behavior is documented.
- [ ] `make ci` passes from a clean checkout.
- [ ] The pull request includes concrete test evidence and any residual risk.

The [engineering contract](engineering-contract.md) defines the invariants that
these criteria protect.
