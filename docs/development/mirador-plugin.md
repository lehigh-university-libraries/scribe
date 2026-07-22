# Mirador plugin development

`mirador-scribe` is a reusable Mirador 4 package. The app imports its source in
development, while the package also produces distributable ESM and CommonJS
artifacts.

Keep the plugin thin:

- backend RPCs own canonical structural mutations;
- a typed editor-session reducer owns draft, history, rebase, and conflict state;
- one geometry module owns OpenSeadragon screen/image conversions;
- browser events are bridged through the typed `DocumentEventMap` contract;
- IIIF updates preserve unknown page, target, selector, and body properties.

Do not parse canonical IIIF JSON with a bare `JSON.parse` or serialize an editor
page with a bare `JSON.stringify`. The shell's IIIF parser uses the reviver
source token to mark numbers that JavaScript would round, and the adapter's
`stringifyIIIFJSON` restores those tokens with `JSON.rawJSON`. The wrappers are
deliberately compatible with `structuredClone`, so reducer history and rebases
do not weaken the unknown-property round-trip contract.

`npm run check:types` checks the implementation, not only the package
declaration: JavaScript and JSX run through TypeScript `checkJs`, and the
stateful browser boundaries use explicit JSDoc state/ref types. Shared shell
bridge payloads and the corresponding `DocumentEventMap` live in
`src/index.d.ts`. Add a named event contract there before emitting a new event
that crosses between the plugin and application shell.

Every bridge payload carries the exact Mirador `windowId` and IIIF `canvasId`.
Background processing payloads additionally carry `itemImageId`. Receivers
must require all applicable identities; an omitted ID is never interpreted as
a broadcast. Mirador's current window Canvas is authoritative during page
turns—an annotation selection from the prior Canvas is not a routing fallback.

Line creation has two scoped bridge paths. Pointer drawing emits
`scribe:create-annotation` after a drag. The keyboard action emits
`scribe:create-line-at-viewport-center`; the viewport creates a bounded,
line-shaped rectangle in the visible image and then emits the same creation
event with a requested resize handle. After the draft enters the reducer,
`scribe:focus-resize-handle` moves focus to that handle. Its ARIA label and
`aria-keyshortcuts` expose Arrow-key resizing, with Shift selecting larger
steps. All three event payloads are declared in `src/index.d.ts` and remain
scoped to one Canvas and Mirador window.

Draft annotations use the canonical page namespace
`<triplet-presentation-base>/item-image-<positive-id>/canvas/page-1/annotations/items/<32-lowercase-hex>`.
The client rejects any loaded page ID outside
`/item-image-<positive-id>/canvas/page-1/annotations` before drawing. Structural RPCs
receive the adapter item-image ID, the complete current draft page, and selected
annotation IDs, then return a complete replacement page. Companion-window code
must push that page directly instead of implementing remove/merge rules. This
allows structural transforms before the first save and prevents the server from
rekeying a draft while a newer local edit is in flight.

Run `make test-frontend` after a plugin change and `make test-browser` whenever
session, geometry, focus, or persistence behavior changes. The latter runs real
Chromium and mounts Mirador with the Scribe plugin against a two-Canvas IIIF
Presentation 3 fixture in addition to exercising the production shell layout,
session reducer, geometry module, and annotation adapter. In CI, the persistence
scenario uses the real Connect handler and an isolated MariaDB fixture. It also
submits a complete draft containing a newly drawn, not-yet-saved canonical
annotation to a structural RPC before exercising atomic save, reload, and
revision-conflict behavior.
