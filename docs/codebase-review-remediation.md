# Codebase Review and Remediation Report

Date: 2026-05-14

## Scope

Review skills applied across the full codebase:

- distributed-systems-expert
- devops-expert
- go-expert
- security-review
- vault-expert

Frontend review applied to `web`:

- frontend-expert

Findings were synthesized through lead-architect and remediated in-place. This project was treated as reinstallable, so fixes removed stale compatibility paths instead of preserving old behavior.

## Review Loop 1 Findings

| ID | Area | Finding | Status |
| --- | --- | --- | --- |
| R1-01 | API contract/authz | `PublishItemImageEdits` was a manual Connect-like HTTP route outside the protobuf contract and was mapped to `annotations:read` despite publishing edits. | Fixed |
| R1-02 | Security/Vault | Provider secret responses exposed `vault_path` to frontend clients. | Fixed |
| R1-03 | Go/API cleanup | Disabled hOCR filesystem compatibility helpers remained in the server and call sites still pretended to read/write them. | Fixed |
| R1-04 | Protobuf quality | `AuthMeRequest` and `AuthMeResponse` violated buf RPC naming rules for `GetAuthMe`. | Fixed |
| R1-05 | Supply chain | CI downloaded `yq` with `sudo curl` and no checksum verification. | Fixed |
| R1-06 | Security scanning | gosec reported unsafe upload/temp file handling, broad file permissions, and a hardcoded-secret heuristic hit in Vault auth code. | Fixed |
| R1-07 | Logging | Provider secret read failures logged the internal Vault path. | Fixed |

## Review Loop 2 Findings

| ID | Area | Finding | Status |
| --- | --- | --- | --- |
| R2-01 | Security scanning | The upload write path still needed basename validation at the final filesystem boundary for gosec path traversal analysis. | Fixed |

## Remediation Summary

- Added `PublishItemImageEdits` to `proto/scribe/v1/annotation.proto` with item-image resource authorization at write level.
- Updated Go and web clients to use the generated Connect RPC instead of a handwritten `scribeFetch` call.
- Removed the manual publish route from `internal/server/http.go`.
- Renamed auth messages to `GetAuthMeRequest` and `GetAuthMeResponse`.
- Removed client-facing `vault_path` from `ProviderSecretRecord` while retaining internal database and Vault lookup fields.
- Removed stale hOCR filesystem compatibility functions and all no-op call sites.
- Validated upload filenames before writing to `uploads/`, tightened local upload file permissions, and replaced derived temp output paths with `os.CreateTemp`.
- Stopped logging internal Vault paths on provider-secret read failures.
- Added SHA-256 verification for `yq` downloads in `.github/workflows/build-ocr.yaml`.
- Updated generated Go and TypeScript protobuf artifacts.
- Refreshed Terraform provider lock hashes while initializing providers for validation.

## Validation Results

Latest successful checks:

- `buf lint proto`
- `go test ./...`
- `gosec -exclude-generated ./...`
- `govulncheck ./...`
- `npm --prefix web test`
- `npm --prefix web run build`
- `CI=true npm --prefix mirador-scribe run build`
- `npm --prefix web audit --audit-level=moderate`
- `npm --prefix mirador-scribe audit --audit-level=moderate`
- `terraform -chdir=terraform validate`

## Review Loop Status

- Loop 1 found issues R1-01 through R1-07 and remediated them.
- Loop 2 found R2-01 and remediated it.
- Loop 3 reran the same review checks and found no new issues.

## Review Loop 4 Findings

| ID | Area | Finding | Status |
| --- | --- | --- | --- |
| R4-01 | Connect authz | Streaming RPCs did not pass through authz, panic recovery, or structured logging interceptors. | Fixed |
| R4-02 | Distributed systems | Transcription job retries/redeliveries restarted from segment 0 instead of persisted progress. | Fixed |
| R4-03 | DevOps | The Pub/Sub transcription DLQ had no monitored subscription or alert. | Fixed |
| R4-04 | Vault | Provider-secret Vault ACLs used per-workspace identity metadata that did not match the single multi-tenant app identity. | Fixed |
| R4-05 | Delivery | Production Terraform applied automatically on every `main` push, leaving the real deployment gate outside repo review. | Fixed |
| R4-06 | Frontend TDD | The editor entry point had no tests around save-before-leave or transcription reload behavior. | Fixed |
| R4-07 | Frontend size | `editor.ts` exceeded the 500-line editor-core threshold. | Fixed |
| R4-08 | Frontend size | `shell.ts` exceeded the 500-line frontend module threshold and concentrated unrelated workflows. | Fixed |
| R4-09 | Frontend TDD | Shell tests did not cover contexts, workspace-member management, or provider secrets. | Fixed |
| R4-10 | Vault | The routine `operator` policy could mutate auth backends, ACL policies, and identity state. | Fixed |
| R4-11 | Scale readiness | SSE polling and in-memory OAuth state were acceptable for one API process but needed explicit scaling triggers. | Documented |
| R4-12 | Frontend hygiene | Tailwind shell styling and MUI/Mirador styling needed an ownership note, and app code should avoid bare lodash imports. | Verified |
| R4-13 | E2E smoke | No repo target exercised the ingest/edit/save/reload persistence path as a containerized smoke check. | Fixed |
| R4-14 | Ops | Single-VM MariaDB recovery assumptions needed an in-repo runbook and explicit Terraform version bound. | Fixed |
| R4-15 | Local exposure | The local Traefik dashboard was enabled by default. | Fixed |

## Loop 4 Remediation Summary

- Implemented `WrapStreamingHandler` for Connect authorization, panic recovery, and request logging.
- Removed the stream handler's redundant manual job ownership check; authz now happens at the Connect boundary.
- Made transcription processing resume from persisted `completed_segments` and `failed_segments`, preventing full-job reprocessing after retries or lease recovery.
- Added a persistent Pub/Sub DLQ monitor subscription and a Cloud Monitoring alert policy for undelivered DLQ messages.
- Simplified provider-secret Vault ACLs to a deployment-scoped prefix and documented Go as the active workspace isolation boundary.
- Changed production Terraform so pushes to `main` run a plan; `apply` now requires manual `workflow_dispatch` plus the production environment gate.
- Added focused editor tests for save-before-leave and transcription-completion reload behavior.
- Split editor layout, Mirador config, and transcription status helpers into small modules so `editor.ts` is no longer over 500 lines.
- Re-split the shell entry into a 500-line orchestration module plus shared render helpers, preserving the existing `renderShell` route entry point.
- Added shell tests for context creation, workspace-member add/update/remove, and provider-secret create/delete workflows.
- Reduced routine Vault operator access to read/list on auth, ACL policy, and identity paths; short-lived break-glass access now owns those mutations.
- Added README scaling notes for per-client SSE polling and per-process OAuth state before horizontal API scaling.
- Added `make e2e-smoke`, which runs the DB-backed manifest ingest and annotation edit/save/reload smoke path in a container.
- Documented backup/restore and DLQ operations, tightened Terraform's upper version bound, and disabled the local Traefik dashboard by default.
- Verified there are no bare `lodash` imports in app source; Tailwind remains the shell owner and MUI/Emotion remains isolated to Mirador.
