# Context catalogs and selection rules

`ContextService.ListContexts` and `ListSelectionRules` are workspace-scoped,
bounded keyset scans. Both requests accept `page_size` (`0` defaults to 50;
maximum 100) and an opaque `page_token`; both responses return
`next_page_token`. Pass continuation tokens back unchanged. They are HMAC-signed
and bound to the authenticated workspace and the request filter, so a token from
another workspace or a different `system_only`/`context_id` filter is rejected.

Context pages are ordered deterministically by default status, system ownership,
and ID. Rule pages are ordered by descending priority and ascending ID. An empty
`next_page_token` ends the scan. The generated web helper exposes page-oriented
functions for incremental interfaces and convenience functions that traverse
all bounded server pages.

```ts
let pageToken = "";
do {
  const page = await contextClient.listContexts({
    pageSize: 50,
    pageToken,
    systemOnly: false,
  });
  consume(page.contexts);
  pageToken = page.nextPageToken;
} while (pageToken);
```

Selection-rule creation is admitted transactionally per workspace. A workspace
may have at most 100 rules visible to its resolver, including any system rules.
Concurrent creates are serialized at the workspace boundary, so the limit
cannot be exceeded by racing requests. Reaching it returns
`resource_exhausted`; delete or consolidate rules before retrying.

`ResolveContext.metadata_json` is a flat JSON object, not an arbitrary document.
It is limited to 64 KiB, 64 scalar fields, 255 UTF-8 bytes per field name, and
4096 UTF-8 bytes per scalar representation. Values may be strings, finite
numbers, booleans, or `null`; arrays and nested objects are rejected. The server
normalizes each scalar once and evaluates at most 100 rules with at most 32
conditions each. These ceilings make resolution cost predictable while keeping
priority and first-match semantics explicit.

Continuation tokens are authenticated state, not encrypted application data.
Do not parse, construct, persist as durable identifiers, or reuse them after
changing a filter or workspace.
