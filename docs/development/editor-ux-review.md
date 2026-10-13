# Transcription editor UX review

This page records the September 2026 review of the Mirador correction editor
(`mirador-scribe` plus the `web/` editor page), the pain points it found while
walking the common correction tasks, and the decisions that were implemented.
Keep it current when an interaction contract changes; the invariants it
describes are the ones `make test-frontend` and `make test-browser` protect.

## Method

The review traces upload and existing-page correction flows through the source
and automated tests. Chromium acceptance covers pointer and keyboard behavior;
DOM assertions cover accessible labels and status announcements. This evidence
does not constitute a manual screen-reader audit. Findings are grouped by task
in the order a person meets them.

## Findings and decisions

### Upload to editor

| Finding | Decision |
| --- | --- |
| After the upload handoff the wand animated over each line, but when the job finished the page showed the image alone. Nothing indicated the text had arrived until the person found the overlay button. | When transcribed text arrives on a page whose overlay is off, the read overlay turns on once for that Canvas. An explicit mode choice afterwards is never overridden. Opening an already transcribed page behaves the same way. |
| Re-running the pipeline with a different context required leaving the editor, changing the library selector, and reprocessing from the item card. | The editor header has a processing-context selector preselected to the context that produced the current run. "Reprocess page" (and Alt+R from the plugin) re-segments and retranscribes the whole page with that context, saving pending edits first, then adopts the successor job and context. |
| The plugin also offered a foreground "retranscribe selected lines" dialog that called the enrichment RPC line by line, bypassing the durable job, its fencing, and its progress UI. | Removed. Repeat transcription is whole-page reprocessing or nothing; the plugin asks the shell through the typed `scribe:request-reprocess` event. The adapter keeps its `transcribeAnnotation` API for package consumers. |

### Selecting and correcting words

| Finding | Decision |
| --- | --- |
| A word on the image could be clicked only in the "read" overlay. In "off", "outline", and even "edit" mode the boxes were `pointer-events: none`, so changing a different line meant Tab-stepping or switching modes first. | Every visible line and word is a hit target in every mode except "read" (where the text labels already are). A click selects the word, opens the inline editor, and focuses that word's input. Hit targets are `aria-hidden` and untabbable; the labels and inputs remain the accessible path. |
| A quick click on a word zoomed the image, because OpenSeadragon's click-to-zoom fired underneath the overlay. React `stopPropagation` never reaches OpenSeadragon: both listen on the same node in the bubble phase. | The shell disables `clickToZoom` (double-click still zooms). Independently of the shell, the plugin installs an OpenSeadragon `preProcessEventHandler` so elements marked `data-scribe-interactive` opt out of viewer gestures while keeping browser defaults (focus, caret, text selection), and quick clicks on `data-scribe-hit` targets never zoom while drags through them still pan. |
| Mouse navigation was disabled for the whole viewer while the inline editor was open, so people could not pan or zoom to the next line without leaving edit mode. | Navigation stays on in every mode; the gesture guard above is what keeps the inputs and handles from panning the image. |
| Enter in the inline editor saved the page, which is surprising while moving through a line; there was no keyboard route to the next line other than Tab. | Unchanged for the inline editor (Enter saves, Tab steps rows) because the browser suite and existing users depend on it. The new transcript pane uses Enter and Arrow keys to step rows and Ctrl/Cmd+Enter to save. |

### Adding lines and words

| Finding | Decision |
| --- | --- |
| Line creation needed the pointer ("Draw line") or a toolbar click for the centred line; word creation was toolbar only. | `N` adds a centred line and `W` adds a word beside the selection, outside text fields. Both appear on the controls' `aria-keyshortcuts` and in the legend. |
| A draft word was created with an approximate box and no way to place it. | The draft word is focused, and the geometry handles now follow the focused word (see below), so it can be placed immediately with the pointer or Arrow keys. |

### Moving and resizing boxes

| Finding | Decision |
| --- | --- |
| Only the selected line had resize handles; a word box could not be adjusted at all, and neither could be moved without resizing two corners. | The dashed preview and handles follow the focused word when there is one, otherwise the selected line. A "Move" grip above the box drags or Arrow-nudges the whole box (Shift for 10 px). Corners resize as before. The `resize-annotation` payload now carries `operation: "move" \| "resize"` so the two are counted separately. |
| Dragging a word past its line, or a line past the image edge, produced geometry the server would later reject or re-parent. | The geometry module clamps: a word stays inside its owning line and a line inside the canonical image. The clamp shrinks a box that is larger than its container instead of pushing it outside, and a box is never smaller than one pixel. |

### Deleting

| Finding | Decision |
| --- | --- |
| The toolbar's Delete removed the selected annotation while Ctrl+Backspace removed the focused word, so the same page state deleted different things depending on the input device. | One delete target: the focused word when there is one, otherwise the selected annotation. The button is labelled "Delete word" or "Delete line" with the text in its accessible name, plain `Delete` works outside text fields, and the status message says what was removed and that Undo restores it. |

### Splitting and joining

| Finding | Decision |
| --- | --- |
| "Split to words", "Split line", "Join words", and "Join lines" already went through the structural RPCs with a complete draft page and were well fenced. The status after "Split to words" did not tell people what to do next. | Kept as they were; the success message now points to clicking any word on the image. Every structural operation is recorded in the edit metrics. |

### Overlay modes

| Finding | Decision |
| --- | --- |
| One button cycled through four modes and showed only the current one, so reaching a specific mode took up to three clicks and gave no overview. | An exclusive mode switch ("Off", "Edit", "Read", "Outline", "Transcript") with `aria-pressed`, tooltips describing each mode, and single-key shortcuts (Esc, E, R, T). |
| There was no way to read the transcription as text while seeing the image, short of the small inline editor for one line. | New "Transcript" mode: the image keeps the left of the viewer and a pane on the right lists one editable row per line, in canonical reading order in a scrollable flowing list. Rows have a fixed minimum height and 15px type at every zoom. The pane reserves its width through OpenSeadragon viewport margins, so coordinate conversion stays exact. Focusing a row selects the line and pans (never re-zooms) only when the line is out of view. |

### Metrics

| Finding | Decision |
| --- | --- |
| The server already computed a Levenshtein distance from the OCR baseline on every save and stored it on the OCR run, but nothing reached the person editing, and there was no record of what an edit session did. | `SaveAnnotationPageResponse` now returns `AnnotationCorrectionMetric` (distance plus baseline and corrected code-point counts) when a baseline exists. The plugin diffs the base revision against the saved page (lines and words added, deleted, retyped, boxes moved or resized, character distance over lines) and counts editor operations since the previous save. Both, with the server metric, go out as `scribe:edit-metrics`; the save status reads "Saved page: 2 words retyped, 1 box moved." and the header shows the distance from the model baseline. |

## Contracts added or changed

- `ScribeOverlayMode` gains `transcript`.
- `scribe:edit-metrics` (`ScribeEditMetricsEventDetail`) and
  `scribe:request-reprocess` (`ScribeReprocessRequestEventDetail`) are typed in
  `mirador-scribe/src/index.d.ts`.
- `scribe:select-annotation` accepts `overlayMode`; `scribe:inline-change-text`
  accepts `annotationId`; `scribe:focus-annotation` accepts `ensureVisible`;
  `scribe:resize-annotation` carries `operation`.
- The geometry module owns `resizeBBoxFromHandle`, `translateBBox`,
  `clampBBoxWithin`, `imageBoundsBBox`, and `viewportOffsetToReveal`.
- `viewerGestures.ts` owns the OpenSeadragon gesture guard and the
  `data-scribe-interactive` / `data-scribe-hit` attribute contract.
- `editMetrics.ts` owns the page diff and operation counters.

## Verification

Review regressions also cover complete transcript text under horizontal zoom,
ignored edits to retired rows, explicit overlay choices before OCR completion,
context-selector keyboard routing and stale catalog responses, preserved shell
focus during viewer animation, negative viewport origins, and bounded metrics
that never silently truncate long-line edits.

- `mirador-scribe`: Vitest covers the geometry clamps, the metrics diff, the
  gesture guard, keyboard routing, hit-target clicks versus drags, word move and
  resize clamping, the transcript pane reading order, non-overlapping flow, fit and margin, delete-target
  resolution, the auto read overlay, the reprocess request, and the
  `scribe:edit-metrics` payload after a save.
- `web`: Vitest covers the header context selector, reprocessing with the
  chosen context and adopting it for later adapters, the scoped reprocess
  request, and the metrics line.
- Go: the DB-backed context-metrics acceptance test asserts the correction
  metric on the save response.
- `make test-browser` and the deployed readiness script drive the mode switch,
  the reprocess control, and the renamed resize handles in real Chromium.
  Save assertions accept the correction summary in the live status message;
  deployed readiness also verifies the saved canonical annotations.
  Local Chromium and deployed readiness share save-success and responsive
  geometry checks, including visibility of all 18 primary toolbar actions.
  The DB-backed full-editor check retains the completed-transcription status
  while resizing through the deployed viewports, so header content and inherited
  button spacing cannot silently clip the toolbar in landscape.
