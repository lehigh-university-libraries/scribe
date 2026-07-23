# Mirador Scribe

Mirador 4 plugin for editing Scribe's canonical IIIF Presentation 3
`AnnotationPage` resources.

## Persistence contract

The plugin treats an entire AnnotationPage as the persistence unit. Its adapter
runtime must provide an authenticated client and the tenant-scoped item image:

```ts
new ScribeAnnotationAdapter(endpoint, 3, canvasId, user, {
  client,
  itemImageId: "42",
  contextId: "7",
  windowId: "scribe-editor-window",
});
```

`client.getAnnotationPage(itemImageId)` returns `{ page, revision }` and
`client.saveAnnotationPage(itemImageId, json, expectedRevision)` atomically
saves it. A stale revision must fail with Connect `aborted`; the editor never
falls back to a sequence of per-annotation writes.

Mirador's native adapter `create`, `update`, and `delete` methods dispatch the
documented `scribe:annotation-mutation` event to the mounted companion window.
That bridge updates the same local reducer draft and returns the projected page
to Mirador; it never reads or writes the server. `savePage` is called only by an
explicit page Save (including the save-before-publish workflow). The event is
scoped by exact `windowId`, Canvas ID, and item-image ID, so two Mirador windows
cannot consume the same mutation.

## Editor state

`src/editor/session.js` owns the last confirmed server page, the local draft,
compact annotation-level undo/redo patches, load/save/conflict lifecycle, and
three-way rebasing of transcription results. The companion keeps
one of these sessions per exact Canvas ID through `src/editor/sessionCache.ts`,
so an unsaved draft survives A → B → A navigation and can never be merged into
another AnnotationPage. Mirador's Redux annotation state is a rendered
projection, not the save baseline. Structural operations send the adapter's
`itemImageId`, the complete current draft page, and selected annotation IDs to
pure backend RPCs. The backend always returns a complete page, so the plugin
never reconstructs split/join semantics from response fragments. Because a
response may be delayed, the reducer applies the submitted-to-result
page patch onto the latest draft. Later local edits and background rebases are
preserved, and overlapping IDs are surfaced as pending conflicts.

New draft IDs are generated only after a page with the canonical
`<triplet-presentation-base>/item-image-<positive-id>/canvas/page-1/annotations`
ID has loaded. Their
full shape is `<page-id>/items/<32-lowercase-hex>`, so a complete draft that
contains them can be sent to authorized structural RPCs before the first save.

Every editor event includes the exact `windowId` and `canvasId`; item-level
background events also include `itemImageId`. New event handlers must reject
events for other windows, Canvases, or item images. Missing identity is not a
global broadcast. Cross-package payloads and their `DocumentEventMap` entries are
declared in `src/index.d.ts`; `npm run check:types` checks all implementation
TypeScript/TSX and checked JavaScript/JSX as well as that public declaration.
`npm run build` also compiles a package-consumer fixture against the emitted
declarations, then smoke-loads both runtime formats. When the focused Mirador Canvas changes, the companion window
emits `scribe:active-canvas` with this stable bridge payload:

```ts
{
  canvasId: "https://example.org/manifest/canvas/2",
  itemImageId: "42", // empty only for a non-Scribe external adapter
  windowId: "window-id",
}
```

Shell actions such as reprocess and publish must use this active item-image ID,
not the first page in the manifest or the item image from the editor URL.

Dirty state is aggregated across every Canvas cached by a companion window:

```ts
// scribe:dirty-state
{
  activeCanvasId: "https://example.org/manifest/canvas/2",
  dirty: true,
  dirtyCanvasIds: ["https://example.org/manifest/canvas/1"],
  windowId: "window-id",
}
```

A window-scoped `scribe:request-save` saves every dirty cached Canvas serially. Its
`scribe:save-result` has `ok: true` only when the whole cache is clean; it also
includes `dirtyCanvasIds` so a caller can keep navigation blocked when an edit
arrived during the save or a Canvas failed.

## Development

```bash
npm ci
npm test
npm run check:types
npm run build
```

The build emits native `dist/mirador-scribe.mjs` and
`dist/mirador-scribe.cjs` entry points and smoke-loads both formats. Clean
Canvas sessions use a bounded LRU; dirty sessions are retained until saved or
explicitly discarded.

Add behavior tests beside deep modules. Geometry code belongs in
`src/editor/geometry.ts`; IIIF-preserving mutations belong in
`src/utils/iiif.js`; persistence and revision behavior belongs in
`src/editor/session.js` or the adapter.
