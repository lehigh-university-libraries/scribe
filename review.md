# Scribe — Codebase Review (Round 3)

**Date:** 2026-07-16
**Reviewer:** Claude (Opus 4.8), go-expert + distributed-systems + devops + vault rubrics applied.
**Scope:** Re-review after the round-2 findings (N1–N5) were reported fixed. Verify the fixes landed and are correct, then sweep for regressions the fixes may have introduced.

---

## 1. Verification of Round-2 Fixes

| ID | Round-2 issue | Status | Evidence |
|----|---------------|--------|----------|
| N1 | mirador-scribe persistence round-trip untested | ✅ Fixed | `mirador-scribe/src/annotationAdapter/ScribeAnnotationAdapter.test.js` does edit → new adapter → `all()` → assert bbox+text survived; wired into `ci/test-frontend.sh` |
| N2 | `-race` not confirmed in CI | ✅ Fixed | `ci/test.sh:33` runs `go test -v -race ./...` |
| N3 | Duplicate `ocr_runs` rows skew metrics; seed empty jobs | ✅ Fixed | Dedup query + composite index are correct; Round-3 regression is now fixed by passing the actual completed count into OCR-run seeding and testing the path |
| N4 | Retention only prunes after first tick; webhook retention gated on webhooks | ✅ Fixed | `events.go:79,127` prune immediately on startup; `startWebhookEventRetention` runs independent of `webhookURLs` |
| N5 | `serviceAccountKeyAdmin` grant too broad | ✅ Fixed | `vault.tf` replaces it with custom role `vaultGcpAuthKeyVerifier` granting only `iam.serviceAccountKeys.get` — the correct minimum for Vault GCP `iam` auth |

The dedup metrics query (`sqlc/queries/ocr_run_metrics.sql`), the `(context_id, item_image_id)` index, the vault least-privilege role, and the retention startup-prime are all correct, well-shaped fixes.

---

## 2. New Findings

### HIGH

#### H-new — ✅ Resolved: N3's "skip empty jobs" guard silently disabled OCR-run seeding for **every** freshly-completed async transcription job (regression of H2)

`internal/server/transcription_service.go:593`

```go
if job.CompletedSegments == 0 {
    return nil
}
```

This guard was added by the N3 fix to "skip seeding when completed == 0". But it reads the **stale in-memory** `job.CompletedSegments`, not the count the job actually completed.

- `processTranscriptionJob` tracks progress in a **local** `completed` variable (`transcription_service.go:483,548`), pushing each increment to the DB via `UpdateProgress`. The in-memory `job` struct's `CompletedSegments` field is **never reassigned** after the job is claimed (only assignment in the package is at load time, `internal/store/transcription_jobs.go:582`).
- A fresh job is claimed with `CompletedSegments == 0`. After successfully transcribing all N segments, `job.CompletedSegments` is **still 0** when the guard runs at line 593.
- Result: `seedTranscriptionJobOCRRun` returns `nil` and **never writes the `ocr_runs` baseline** for any fresh async job — which is exactly the bug round-1 finding **H2** was created to fix. `GetContextMetrics` therefore undercounts async transcriptions again. Resumed jobs (which enter with `CompletedSegments > 0`) still seed, which will make the regression look intermittent rather than total.

**Why it slipped through:** review.md round-1 claimed H2 was "covered by `transcription_service_test.go`", but there is **no test** exercising `seedTranscriptionJobOCRRun`'s completion path (the file only tests audit retention, context resolution, and `resumedTranscriptionProgress`). The guard's own unit behavior is unverified.

**Resolution:** `processTranscriptionJob` now passes the local `completed` count into `seedTranscriptionJobOCRRun`, and the guard checks that actual count instead of the stale job struct. `TestSeedTranscriptionJobOCRRunUsesActualCompletedCount` covers both seeding after a completed job and skipping when the completed count is zero.

### LOW

#### L-new-1 — ✅ Resolved: Dedup grouping key can collide `item_image_id` with a numeric `session_id`

`sqlc/queries/ocr_run_metrics.sql` groups by `COALESCE(CAST(item_image_id AS CHAR), session_id)`. `session_id` is the table's `VARCHAR(128)` primary key and can be client-supplied; a manual run with `session_id = "5"` would collapse into the same group as all runs with `item_image_id = 5`, slightly biasing the deduped averages. Edge case, but a composite key like `COALESCE('img:'||CAST(item_image_id AS CHAR), 'sess:'||session_id)` removes the ambiguity cheaply.

**Resolution:** The metrics query now groups with an explicit `CASE` key using `img:` and `sess:` prefixes, and the checked-in generated query string is kept in sync.

#### L-new-2 — ✅ Resolved: Seed test coverage still missing (see H-new)

The single test that would have caught H-new — a fresh-job-to-`ocr_runs`-row assertion — does not exist. This is the highest-value backend test to add and directly protects the H2/N3 code path.

**Resolution:** Added `TestSeedTranscriptionJobOCRRunUsesActualCompletedCount`.

---

## 3. Bottom line

N1, N2, N3, N4, and N5 are now closed. The Round-3 regression in OCR-run seeding is fixed, seed coverage has been added, and the metrics dedupe key no longer collides numeric session IDs with item image IDs.
