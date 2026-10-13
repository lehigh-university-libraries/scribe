# Mirador plugin development

`mirador-scribe` is a reusable Mirador 4 package. The app imports its source in
development, while the package also produces distributable ESM and CommonJS
artifacts.

The editor gives action buttons most of the toolbar width. The keyboard shortcut
key stays compact beside them, wraps below on narrow screens, and hides when
the viewport is too short.
Compact buttons set their line height and vertical padding explicitly so the
complete toolbar fits even when the editor header shows a transcription status.

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
Transcription progress also carries the durable job attempt number. Live
progress and completed-job catch-up keep separate attempt-scoped visual history,
so failed-attempt ordinals cannot hide a successful retry. When the active wand
line falls outside the current OpenSeadragon image bounds, the overlay emits the
existing scoped focus event; a line already in view never moves the viewport.

Line creation has two scoped bridge paths. Pointer drawing emits
`scribe:create-annotation` after a drag. The keyboard action emits
`scribe:create-line-at-viewport-center`; the viewport creates a bounded,
line-shaped rectangle in the visible image and then emits the same creation
event with a requested resize handle. After the draft enters the reducer,
`scribe:focus-resize-handle` moves focus to that handle. Its ARIA label and
`aria-keyshortcuts` expose Arrow-key resizing, with Shift selecting larger
steps. All three event payloads are declared in `src/index.d.ts` and remain
scoped to one Canvas and Mirador window.

Draft words created beside a selected word are bounded by that word's owning
line. If the selection already reaches the line's right edge, the new draft
uses a visible one-pixel box at that edge instead of producing geometry beyond
the canonical image. The geometry module owns and tests this boundary rule.

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

## Overlay modes and interaction

The overlay mode is one exclusive choice: `none`, `edit`, `read`, `outline`, or
`transcript` (`ScribeOverlayMode`). The action panel renders it as a toggle
group with `aria-pressed`; the shortcuts are Esc, E, R, and T. When transcribed
text first arrives on a Canvas whose overlay is off, the companion window turns
the read overlay on once so a finished upload shows its text; an explicit mode
choice is never overridden afterwards.

Every visible line and word is a hit target (`data-scribe-hit`) in every mode
except `read`, where the text labels already are. A hit target commits a
selection on a release that did not travel; a drag through it still pans. The
selection event carries `overlayMode` so the transcript pane keeps its mode and
every other origin opens the inline editor with the clicked word focused.

Mouse navigation stays enabled in every mode. `editor/viewerGestures.ts`
installs an OpenSeadragon `preProcessEventHandler` on the viewer's inner
tracker: elements marked `data-scribe-interactive` opt out of viewer gestures
while keeping browser defaults, and quick clicks on hit targets never zoom.
React `stopPropagation` cannot do this because OpenSeadragon and React listen
on the same node in the bubble phase. The shell also disables click-to-zoom in
its OpenSeadragon options; double-click still zooms.

Geometry handles follow the focused word when there is one, otherwise the
selected line. A "Move" grip drags or Arrow-nudges the whole box (Shift for a
10 px step); the corner handles resize. The geometry module clamps a word to
its owning line and a line to the canonical image (`clampBBoxWithin`), and the
`scribe:resize-annotation` payload carries `operation: 'move' | 'resize'`.

`transcript` mode reserves roughly 45% of the viewer on the right (minimum
260px where space permits). It shows every line in canonical AnnotationPage
reading order in a scrollable list with 32px rows and 15px text. Rows never
follow image y coordinates. Selecting a row highlights and reveals its image
line without zooming; selecting an image line scrolls and focuses its row.
Entering the mode, loading a Canvas, and resizing fit the full page with
OpenSeadragon's right viewport margin. User pan/zoom keeps the text layout
stable.

Deletion resolves one target: the focused word if any, otherwise the selected
annotation. The button label names it ("Delete word", "Delete line") and plain
Delete works outside text fields.

## Reprocessing and edit metrics

Line-level foreground retranscription is not offered. The plugin's "Reprocess
page" action and Alt+R emit `scribe:request-reprocess` with the exact window,
Canvas, and item image; the shell re-segments and retranscribes the whole page
with the processing context selected in its header, saving pending edits first,
then adopts the successor job and context for later adapters.

After every successful save the companion window emits `scribe:edit-metrics`:
`metrics` is the diff between the replaced base revision and the saved page
(lines and words added, deleted, retyped, boxes moved or resized, character
distance over lines, from `editor/editMetrics.ts`), `operations` counts the
editor interactions since the previous save, and `correction` is the server's
Levenshtein distance from the OCR baseline as returned by
`SaveAnnotationPageResponse.correction`. The save status message includes the
summary; the shell shows it with the baseline distance in the editor header.
The browser character distance is exact or `null` when a changed line exceeds
the computation budget after removing common prefixes and suffixes. The server
baseline metric remains authoritative. Context choices reset on Canvas changes,
and late catalog responses cannot replace the active Canvas's choices.
See the [editor UX review](editor-ux-review.md) for the reasoning.
