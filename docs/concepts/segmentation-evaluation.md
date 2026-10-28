# Segmentation evaluation and ground truth

Contexts select a document type from htr's profile registry: `generic`,
`printed`, `newspaper`, `manuscript`, `letter`, `ledger`, or `form`. This selects
the requested layout features and the segmentation measurement policy. The
weights are uncalibrated until there are enough checked pages to compare them
with transcription accuracy.

An item's **Document type** control overrides its context for future
processing. Empty means inherit. Queued jobs retain the resolved type in their
immutable context snapshot; changing an item does not change an admitted job.

## Mark a checked revision

Save all edits, then select **Ground truth…** in the editor. Confirm **Mark**
only after checking both text and layout. The action records that exact
canonical revision, its original model segmentation, transcript, profile,
image URL, actor, and timestamp. A stale revision fails explicitly. The same
control unmarks the current revision.

Editing a marked page creates a new, unmarked revision. The previous checked
snapshot stays immutable; explicitly mark the new revision when it is ready.
Snapshots count toward storage quotas and are removed with the item or image.

Scribe stores only the current editable AnnotationPage, so immutable marked
revisions live in `annotation_ground_truth`, rather than adding another
editable annotation store or pretending that historical revisions already
exist. `ocr_runs.original_page` retains the original htr geometry where available.

## Export and score

Select **Export ground truth** on an item or context. The ZIP contains
`pages.csv`, canonical IIIF sources, dimensioned htr ground-truth pages, the
original segmentation, transcripts, and image URLs. No remote images are
downloaded during export. An export is limited to 100 revisions and 32 MiB of
source payload; select a smaller item or context when it exceeds that limit.

Extract the ZIP and run:

```bash
htr segeval-external --csv pages.csv
```

The CSV points at dimensioned htr JSON so scoring needs no image download and
does not mistake the furthest annotation edge for the full image dimensions.
The unchanged canonical IIIF page is included alongside it.

## Correction telemetry and geometry

Saving a page computes segmentation agreement with its original OCR baseline
alongside character correction distance. The component report records the
profile, geometry level, calibration status, and any fallback. Handwriting
profiles use baseline scoring only when real baselines exist; box-only pages
report the PRImA fallback. Export-time word inference never supplies scoring
ground truth.

Interactive scoring is limited to 256 elements per side, reduced to 32 for
line-level pages with baselines (2048 input and 4096 sampled baseline points per
side), and two concurrent
evaluations per process. Larger pages remain editable and report that offline
scoring is required. The complete bundle remains available for CLI evaluation.

Canonical annotations retain polygons and baselines in an SVG selector beside
the authoritative `xywh` selector. Moving or resizing a box transforms existing
points. Fresh lines have no guessed baseline. Splitting cuts the baseline at
the text split and maps it into each derived box. Joining concatenates real
baselines only when every input has one; the merged line uses its union box
instead of inventing a polygon. Region classifications appear in the editor's
**Region types** controls and are saved with the normal draft.
