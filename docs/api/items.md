# List workspace items

`ItemService.ListItems` is the bounded library-discovery API. It returns items
from the authenticated workspace in descending `(created_at, id)` order. Each
result is an `ItemSummary`: bounded scalar metadata, the total image count, and
at most the first ordered image as `preview_image`. Use `GetItem` when a caller
needs the complete ordered image list.

The request fields are:

- `page_size`: optional; `0` uses the default of 50 and the maximum is 100.
- `page_token`: optional opaque continuation token from the previous response.
- `query`: optional, trimmed, case-insensitive literal substring search over
  item name, ID, and source type; the maximum is 200 Unicode characters.

The response contains `items` and `next_page_token`. An empty
`next_page_token` means the scan is complete. Pass tokens back unchanged; they
are HMAC-signed and tied to the workspace and normalized query that produced
them. A token is rejected if either value changes or if its payload is altered.
Tokens are continuation state, not encrypted application data.

```ts
let pageToken = "";
const query = "marginalia";
do {
  const page = await itemClient.listItems({ pageSize: 50, pageToken, query });
  consume(page.items);
  pageToken = page.nextPageToken;
} while (pageToken);
```

Keyset pagination intentionally does not report a total count. Items created
ahead of the current cursor while a caller walks older pages are visible at the
beginning of a later fresh scan. A continuation walk is not a multi-request
database snapshot: deletes and deliberately backdated inserts may affect later
pages. The stable `(created_at, id)` boundary prevents offset drift and duplicate
rows from ordinary concurrent inserts without making the response grow with the
workspace library.
