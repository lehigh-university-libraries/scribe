# Code generation

Sources and generated outputs are:

| Source | Generator | Output |
| --- | --- | --- |
| `proto/**/*.proto` | Buf remote plugins | Go, Connect, TypeScript, OpenAPI |
| `sqlc/queries/*.sql` and database schema | sqlc | `internal/db` |

Install pinned tools and regenerate:

```bash
make install-tools
make generate
```

`make generate-check` regenerates everything and fails when the working tree
changes. CI runs this from a clean checkout, so a missing tool or stale output
cannot silently pass.

The generated Connect OpenAPI 3.1 document is
`docs/api/scribe.openapi.yaml`. Buf first emits the Connect paths and schemas;
the pinned postprocessor then derives metadata and per-operation security from
the protobuf `authz` method options. Server-streaming RPCs are included. Never
hand-edit the document or maintain a second authorization table for it.

IIIF JSON schemas come from
`github.com/libops/iiif-spec`; they describe IIIF resources, not Scribe's
Connect RPC surface.
