# Exports

Plain text, hOCR, PAGE XML, and ALTO XML are generated from a committed canonical
AnnotationPage. They are views, not editable persistence stores.

`AnnotationService.ExportAnnotationPage` requires both the tenant-scoped
`item_image_id` and the exact committed `expected_revision`. It returns that
revision with the media type, filename, and bytes. A stale revision fails with
Connect `aborted`; export requests never accept annotation JSON supplied by the
caller.

For a multi-page item, `ItemService.GetItem` returns the ordered canonical
revision vector with the item using one bounded database query. The bundled app
verifies that every image has exactly one revision and calls
`ItemService.PrepareItemExport` with that complete vector. The response is a
short-lived, workspace-bound, signed download URL. `/v1/item-exports/{token}`
checks the metadata digest before loading page payloads, fails if any canonical
revision changed, and then creates one bounded text or ZIP response in an
immediately unlinked temporary file. A process crash therefore cannot leave
transcription plaintext in the container filesystem.

Private IIIF Canvas `seeAlso` links use
`/v1/item-images/{item_image_id}/annotations/revisions/{revision}/hocr` so the
linked hOCR is revision-specific. Page, item, prepared-download, and hOCR
exports require `annotations:read`; `items:read` alone never exposes canonical
transcription text.

Exports have dedicated global and per-workspace concurrency limits. A canonical
page may emit at most 32 MiB, one item may read at most 64 MiB of canonical
source and stage at most 128 MiB of derived output, generation has a 90-second
work deadline plus a bounded response-write grace period, and prepared URLs
expire after five minutes.

Golden fixtures exercise the production renderer for each format. PAGE output
conforms to the pinned PRImA PAGE Content 2019-07-15 schema, and ALTO output
conforms to the pinned Library of Congress ALTO 4.4 schema. The backend test
gate verifies the committed schemas by checksum and validates every PAGE and
ALTO golden offline with `xmllint`.
PAGE's required `imageFilename` is the deterministic derivation marker
`source-image.png`; authoritative image provenance remains the target Canvas in
the canonical IIIF AnnotationPage.

When an annotation mutation changes export semantics, update the relevant
implementation and intentionally regenerate the golden file with review of the
diff and schema result.
