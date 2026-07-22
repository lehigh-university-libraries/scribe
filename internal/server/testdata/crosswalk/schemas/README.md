# OCR export schemas

These are LF-normalized copies of the official primary XML schemas used by
Scribe's PAGE and ALTO export acceptance gate. `SHA256SUMS` pins the committed
bytes and `catalog.xml` resolves ALTO's XLink import without network access.

| Schema | Official source | Source SHA-256 |
| --- | --- | --- |
| PAGE 2019-07-15 | <https://www.primaresearch.org/schema/PAGE/gts/pagecontent/2019-07-15/pagecontent.xsd> | `5d7da5af5f5e06d3b9cd1e78b407ffca1862f78ad9823ed89c302fb6409932d5` |
| ALTO 4.4 | <https://www.loc.gov/standards/alto/v4/alto-4-4.xsd> | `5564fd29d2dd090d8102b8a0aa081906afd677cd5ecc632312e56f21ea14702b` |
| LOC XLink dependency | <http://www.loc.gov/standards/xlink/xlink.xsd> | `f1f5bb6003165cdd8f6c1fcc32f8fd1f965e1681010f3b9806d9460bcffa8a3c` |

The source checksums above describe the exact response bytes retrieved on
2026-07-21. The schemas are normalized to LF before commit so Git checkouts are
portable; the checksums in `SHA256SUMS` cover those normalized repository
copies. Updates must come from the same official sources and update both sets
of checksums in one reviewed change.
