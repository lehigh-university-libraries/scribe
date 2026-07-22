# Development guides

The fastest safe extension point is the smallest registry or use-case boundary
that owns the behavior. Avoid provider switches in handlers, duplicating IIIF
mutations in the browser, or adding REST-only business routes.

- [Add a transcription provider](adding-provider.md)
- [Add a segmentor](adding-segmentor.md)
- [Add a Connect RPC](adding-rpc.md)
- [Change the Mirador plugin](mirador-plugin.md)
- [Change the web application](web-frontend.md)
- [Run and write tests](testing.md)
- [Regenerate contracts](code-generation.md)
- [Change the greenfield database schema](database-migrations.md)
