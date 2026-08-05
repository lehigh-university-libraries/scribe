# Editor deep links

The editor URL is a supported integration surface for opening one Scribe item
image directly, without first navigating through the library. Construct it as
a same-origin URL with `URL` and `URLSearchParams`; do not concatenate raw
repository identifiers into a query string.

## Version 1 query contract

The route is `/editor`. Query parameter names and values are case-sensitive.
Changing or removing the documented semantics is a breaking integration
change and requires new browser acceptance evidence.

| Parameter | Requirement | Meaning |
| --- | --- | --- |
| `itemImageId` | Required | Positive base-10 Scribe item-image ID to select. This is the authorization and lookup identity; a Canvas URI is not. |
| `itemId` | Optional | Expected Scribe item ID. When supplied, the editor fails closed if the selected image belongs to another item. |
| `workspace_id` | Required for external links | Positive base-10 workspace ID. It selects the browser workspace before authenticated API calls; it never grants membership. |
| `contextId` | Optional | Positive base-10 processing-context hint. The server-owned context recorded on the existing OCR run or durable job remains authoritative. |
| `autoTranscribe` | Optional | The exact value `1` requests the legacy foreground transcribe-all flow when no durable `jobId` is supplied. Omit it for normal durable jobs. |
| `jobId` | Optional | Positive base-10 durable transcription-job correlation. Its presence tells the editor that batch work is already scheduled; the editor reconciles authorized job state for the selected item image instead of trusting the URL as status or authority. |

When `jobId` identifies a failed durable job, the editor keeps that terminal
state visible with the job's bounded, redacted failure reason. It does not
replace the failure with a generic preparing state or start the legacy
foreground flow. A reload reconciles the same authorized durable job rather
than treating the query parameter itself as evidence that work is active.

For example:

```text
https://scribe.example/editor?itemImageId=4812&itemId=01JABC...&workspace_id=42&jobId=9917
```

Parameter order is insignificant. Unknown parameters are ignored. Integrators
should omit optional parameters they do not use rather than send empty values.
The selected item image, optional item assertion, workspace principal, OCR run,
job, and returned editor Manifest must all agree. A stale or cross-workspace
combination fails closed and offers a return to the library; it must never fall
back to a similarly named Canvas or item in another workspace.

When a page turn selects another Canvas in a multi-image item, the editor
updates `itemImageId` in place and removes the image-bound `jobId` and
`autoTranscribe` hints. It preserves `workspace_id` so a reload remains in the
selected workspace without trying to reconcile the prior image's job. The
exact-job binding is one-shot: returning to the original Canvas uses normal
latest-job discovery and cannot revive the discarded route hint.

## Authentication handoff

A deep link carries location, not authority. Never put an API key, external JWT,
session cookie, or provider credential in its query or fragment. A normal link
requires the reviewer to already have a live Scribe browser session with access
to `workspace_id`; otherwise Scribe runs its normal interactive login flow.
Islandora's machine-to-machine external JWT is not a browser session and must
not be copied into a human-facing URL.

For a reviewer who does not already have a Scribe session, a registered machine
integration calls `AuthService.CreateEditorReviewToken`. The caller must use an
external JWT with a write-capable workspace role and the explicit
`review_tokens:create` scope. Its request contains:

- required `item_image_id`, stable issuer-local `reviewer_subject`, and
  `reviewer_name`;
- optional `reviewer_email`;
- optional `token_ttl_seconds`, default 300 and bounded from 60 through 600;
  and
- optional `session_ttl_seconds`, default 7200 and bounded from 300 through
  28800.

Scribe stores only an issuer-bound digest of the reviewer subject. The response
contains `review_url`, `expires_at`, `workspace_id`, `item_id`, and
`item_image_id`. Treat `review_url` as a short-lived bearer credential: do not
log it, put it through a link shortener or analytics redirect, or expose it to a
chat/email unfurler that may consume one-time links. Prefer a direct browser
redirect from an authenticated Islandora review action. At most 100 unexpired
review tokens may exist in one workspace; handle `resource_exhausted` by
reusing an already issued handoff or waiting for expiry, not by widening the
limit.

The browser GET consumes the URL once, creates an HttpOnly, Secure,
SameSite=Lax review session, and responds with a 303 redirect to a clean
`/editor?itemImageId=...&itemId=...&workspace_id=...` URL. Reuse, expiry, an
item mismatch, or revoked issuer membership fails closed. The resulting
session can review, reprocess, and follow transcription only for that exact
item image; it cannot browse or mutate another item. The redeemed session, not
the query parameters, supplies authority.

## Navigation, not embedding

Open the link as a full-page navigation or a new tab. Scribe sends both Content
Security Policy `frame-ancestors 'none'` and `X-Frame-Options: DENY`; Islandora
must not place the editor in an iframe, modal webview, or embedded object. This
is an intentional clickjacking boundary, not a CORS problem to work around.

The browser contract is exercised by the test
`a raw editor deep link opens the requested item without prior navigation` in
`web/e2e/editor.browser.ts`.
