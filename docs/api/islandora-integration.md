# Islandora integration

Islandora can ingest one repository object into Scribe, follow durable OCR
work, retrieve canonical IIIF or derived OCR formats, and direct a reviewer to
the exact page. Connect/protobuf is the business API; use generated clients or
the generated OpenAPI contract rather than inventing parallel REST routes.

## Register the automation identity

Use a dedicated Islandora OpenID Connect issuer and a dedicated internal Scribe
service user. Do not map an Islandora `sub`, WebID, PID, or Drupal user ID to a
Scribe database user.

1. Configure Islandora to issue short-lived RS256 JWTs with a stable HTTPS
   issuer, a Scribe-specific audience, a non-empty subject, an expiry, a
   key ID, and an allowlisted roles claim. Publish the signing key at a bounded
   HTTPS JWKS endpoint.
2. Bootstrap a dedicated integration account through Scribe's normal OAuth
   login once so the internal user exists; its OAuth credential is not used by
   Islandora afterward. From an administrator's interactive session, call
   `WorkspaceService.AddWorkspaceMember` with that account's email and the
   least workspace role needed, then use `ListWorkspaceMembers` to record the
   positive numeric service-user and workspace IDs. Do not create identity or
   membership rows with direct SQL.
3. Register the issuer under `auth.external_jwt_issuers`. Pin the issuer,
   audience, JWKS URL, workspace, service user, role mapping, and scopes; then
   restart Scribe and confirm readiness. Omitting `jwks_url` uses
   `<issuer>/oauth/discovery/keys`.

An automation identity that ingests, polls, reads exports, and publishes needs
only the corresponding explicit scopes. Webhook subscription management is a
workspace-administrator operation; provision it separately instead of making
the routine ingest identity an administrator. A representative automation
configuration is:

```yaml
auth:
  external_jwt_issuers:
    - issuer: https://islandora.example
      audience: islandora-scribe
      jwks_url: https://islandora.example/oauth/discovery/keys
      workspace_id: 42
      service_user_id: 900
      role_mappings:
        - roles: [scribe_automation]
          role: write
          scopes:
            - items:create
            - items:read
            - items:write
            - transcription:read
            - annotations:read
            - annotations:write
            - review_tokens:create
```

Send the token as `Authorization: Bearer <jwt>`. Scribe verifies the exact
issuer and audience, RS256 signature and `kid`, time claims with a narrow clock
skew, role mapping, current service-user membership, workspace role, and scope
on every request. The registered workspace is authoritative for external JWTs;
a caller cannot select another tenant with `X-Scribe-Workspace-ID`. Rotate the
issuer key through the JWKS endpoint and remove the registration or workspace
membership to revoke access.

## Choose the ingest RPC

Store the Islandora PID in `external_reference_id`. It is a first-class,
workspace-indexed correlation value returned on items and searchable through
`ListItems.query`; keep descriptive or versioned repository data in the
separate bounded metadata JSON.

| Islandora source | Scribe operation | Durable correlation |
| --- | --- | --- |
| One public image URL | `ImageProcessingService.ProcessImageURL` | Stable `idempotency_key`; response item ID, item-image ID, and transcription job ID |
| Existing hOCR plus one image URL or uploaded image body | `ImageProcessingService.ProcessHOCR` | Stable `idempotency_key`; response item ID, item-image ID, and transcription job ID (normally zero because the supplied hOCR is already complete) |
| One or more local files forming one Scribe item | `ItemService.StartUploadBatch`, then one `UploadItemImage` per declared sequence | Stable `batch_id`, immutable file digest declaration, and each upload response's item-image/job IDs |
| One IIIF Presentation 2 or 3 Manifest | `ItemService.ImportManifest` | Stable `idempotency_key`, returned ordered item images, then jobs filtered by each item-image ID |

Use a chosen processing `context_id`, or allow the server's registered
selection rules to resolve one from metadata. A repository request never
supplies a provider URL, Cloud Run audience, model endpoint, or credential.
Manifest import accepts at most the configured Canvas limit (500 by default),
requires every declared Canvas to have a supported public painting image, and
commits the entire item atomically. See [ingest](ingest.md) for URL, hOCR,
upload-resume, size, and failure contracts.

For every operation with an `idempotency_key`, persist one opaque key with the
source operation record and reuse it after timeouts or lost responses. The
same key plus the same request replays the committed result; the same key with
changed input returns `already_exists`. Do not generate a fresh key merely
because an HTTP response was lost.

The `session_id` returned by the two image-processing RPCs identifies OCR
provenance. It is not an authentication session, browser session, or job ID;
use `transcription_job_id` for durable polling.

## Follow jobs and events

Poll `TranscriptionService.GetTranscriptionJob` using the returned job ID. For
manifest imports, use `ListTranscriptionJobs` filtered by each returned item
image when no job ID was returned directly. Treat pending and running as
non-terminal, completed as a signal to load the committed canonical page, and
failed or canceled as terminal outcomes requiring repository policy or human
attention. Per-segment progress is a transient preview, not saved OCR.

For push delivery, have a separate workspace-admin identity with the
`admin:webhooks` scope call `WebhookService.CreateWebhook` for the same
workspace as the ingested item. Supply one public HTTPS target and a 32–1024
byte secret already stored in Islandora's secret manager; the secret is
write-only and Scribe never returns it. Scribe dispatches only that workspace's
events to the subscription. Verify every request signature over the exact raw
body and supplied timestamp before parsing JSON or changing repository state.
Reject malformed or mismatched signatures and timestamps outside a small
replay window. Acknowledge with a 2xx response only after the event ID is
durably recorded. Delivery is retried, so deduplicate by CloudEvent `id` and
make downstream writes idempotent.

Completion, terminal failure, and publication events are CloudEvents 1.0 JSON.
Their data identifies the workspace, Scribe item, item image, Canvas, job or
revision as applicable, and echoes caller correlation such as the external
reference/idempotency metadata. Treat the workspace and resource IDs as a
single authorization tuple, and use the monotonic revision to ignore a stale
delivery. See [events and jobs](events.md) for the wire headers and exact
verification recipe.

## Canonical OCR, exports, and publication

These states are deliberately separate:

| State | Read contract | Meaning |
| --- | --- | --- |
| Transcription job | `GetTranscriptionJob` | Workflow and attempt status; never the canonical OCR payload |
| Committed draft AnnotationPage | `GetAnnotationPage` | Complete workspace-private IIIF Presentation 3 OCR at one monotonic revision |
| Derived export | `ExportAnnotationPage`, or `PrepareItemExport` for an ordered multi-page item | Private plain text, hOCR, PAGE XML, or ALTO XML generated from the exact requested canonical revision(s) |
| Published snapshot | `PublishItemImageEdits(expected_revision)` and its returned public URL | Explicit, revision-checked public Presentation state mirrored to Triplet |

After completion, call `GetAnnotationPage` and retain its revision with the
IIIF AnnotationPage. Pass that same `expected_revision` to
`ExportAnnotationPage` for hOCR or PAGE XML. For an item-wide download, call
`GetItem`, take the complete ordered image/revision vector, and pass it to
`PrepareItemExport`; its signed download URL is short-lived and workspace
bound.

`dev.scribe.transcription.completed` means a new canonical draft was committed.
It does **not** mean the result is publicly dereferenceable. Publication is a
separate side effect: call `PublishItemImageEdits` with the exact reviewed
revision and wait for `dev.scribe.annotations.published` or the successful RPC
response. A revision conflict requires reloading and reviewing the newer page;
never publish whichever revision happens to be current.

## Drive a collection safely

Scribe has item/image/manifest ingest operations, not a collection-wide RPC.
An upload batch and a Manifest import each create one Scribe item; neither is a
container for unrelated repository objects. Drive an Islandora collection with
a bounded client-side queue of independent item operations:

1. Persist one record per Islandora PID containing `external_reference_id`,
   ingest shape, stable idempotency or batch key, Scribe item/image/job IDs,
   latest accepted event ID, and latest canonical revision.
2. Start with concurrency at or below the operator-advertised per-workspace
   processing limit. Reduce it on `resource_exhausted`; retry transient failures
   with exponential backoff and jitter, while allowing unrelated items to
   continue.
3. Reconcile durable state after every restart: replay the same ingest key,
   query upload-batch or job status, then load the canonical revision. Do not
   infer completion from a client process, webhook arrival order, or a count of
   requests sent.
4. Split collections into separate per-item calls. Use one manifest call only
   when those Canvases are intentionally one Scribe item and remain below the
   configured Manifest limit.

This pattern bounds request fan-out, isolates partial failures, and prevents a
retry storm from creating duplicate items or jobs.

## Send a reviewer to Scribe

Construct the supported [editor deep link](deep-links.md) after Islandora has
the Scribe `itemImageId`, item ID, workspace ID, and optional job ID. The link
opens one exact page and carries no credential. Use an existing authenticated
browser session, or have the registered Islandora automation identity call
`AuthService.CreateEditorReviewToken` with its `review_tokens:create` scope.
Supply the exact item-image ID plus the reviewer's stable issuer-local subject,
display name, and optional email. The token link defaults to five minutes
(allowed range 1–10 minutes); its item-image-scoped browser session defaults to
two hours (allowed range 5 minutes–8 hours).

The response returns `review_url`, its expiry, and the bound workspace/item
identities. Redirect the intended reviewer directly to that URL. It is a
one-time bearer credential, so keep it out of logs, analytics, and link-preview
systems. Redemption sets an HttpOnly, Secure, SameSite=Lax cookie and sends a
303 redirect to the clean editor deep link with no token. The review session
can review, reprocess, and follow transcription only for the bound image.
Never place the automation JWT itself in a browser URL.

Scribe's editor is full-page only. It sends Content Security Policy
`frame-ancestors 'none'` and `X-Frame-Options: DENY`, so Islandora must navigate
the current page or open a new tab. iframe or modal embedding is intentionally
unsupported.
