# Documentation workflow

Documentation under `docs/` is the human- and machine-readable source for
architecture, development, API, operations, and release policy. `AGENTS.md` is
a concise repository entry point; durable rules belong in these pages so they
are published and linkable.

## Build and preview

Use the repository targets:

```bash
make docs-build  # strict build into ignored ./site
make docs        # alias for docs-build
make docs-serve  # live reload at http://localhost:8000
```

`make docs-build` builds the digest-pinned `Dockerfile.docs` image, installs
the hash-locked Zensical 0.0.58 dependency graph, and calls `ci/docs.sh`.
Strict mode turns warnings such as broken internal links or missing navigation
entries into failures. Do not add a separate package-install or build sequence
to a workflow.

## Add or move a page

1. Put the Markdown source under the narrowest existing section.
2. Add it to `project.nav` in `zensical.toml`.
3. Link it from the relevant section index and update stale cross-links.
4. Prefer relative links between documentation pages and repository-relative
   code paths in prose.
5. Run `make docs-build`; run actionlint when the publication workflow changes.

Keep operational procedures executable: name the exact command, precondition,
safe scope, success evidence, and recovery path. Do not copy a script into a
page when linking to its stable entry point is clearer.

## Publication

Pull requests build the site in the `infrastructure-and-docs` CI job by calling
`make docs-build`. A push to `main` that changes docs or a docs-build input
triggers `.github/workflows/docs.yml`, which calls that same target, uploads
`./site`, and deploys it through the protected `github-pages` environment.

The workflow has only `contents: read`, `pages: write`, and `id-token: write`;
checkout credentials are not persisted and every action is pinned to an exact
commit. The generated site is never committed.

When changing the docs toolchain, update the Docker base tag/digest,
`requirements-docs.txt` versions/hashes, `ci/docs.sh` expected version,
toolchain behavior tests, and this page together.
