# Development guides

The fastest safe extension point is the smallest registry or use-case boundary
that owns the behavior. Avoid provider switches in handlers, duplicating IIIF
mutations in the browser, or adding REST-only business routes.

- [Add a transcription provider](adding-provider.md)
- [Add a transcription model](adding-transcription-model.md)
- [Add a segmentor](adding-segmentor.md)
- [Add a segmentation model](adding-segmentation-model.md)
- [Add a system context](adding-system-context.md)
- [Add a Connect RPC](adding-rpc.md)
- [Change the Mirador plugin](mirador-plugin.md)
- [Change the web application](web-frontend.md)
- [Run and write tests](testing.md)
- [Regenerate contracts](code-generation.md)
- [Change the greenfield database schema](database-migrations.md)
- [Write and publish documentation](documentation.md)
