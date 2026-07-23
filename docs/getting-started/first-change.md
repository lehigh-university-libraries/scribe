# Make a first change

1. Read the concept and architecture page for the area being changed.
2. Change the source contract first: protobuf for API shape, SQL query/schema
   for persistence, or IIIF domain code for annotation semantics.
3. Add a failing test at the narrowest useful boundary.
4. Implement the behavior.
5. Run `make generate` when protobuf or SQL sources changed.
6. Run the focused tests, then `make ci`.
7. Update the relevant documentation and acceptance criterion.

Do not hand-edit generated files under `internal/db`, `proto/scribe`, or
`web/src/proto`. CI regenerates them and rejects drift.

For behavior involving saved OCR, test the complete invariant: save a canonical
AnnotationPage, reload it from a new client session, dereference the advertised
IIIF resource, and verify an export is derived from the same revision.
