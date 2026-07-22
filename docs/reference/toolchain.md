# Pinned toolchain

| Tool | Version source |
| --- | --- |
| Go | `.go-version` |
| Node.js | `.nvmrc` |
| Python (packaged Kraken/Zensical tooling only) | `.tool-versions` and image build arguments |
| Terraform | `.tool-versions` and workflows |
| ripgrep | `ci/install-ripgrep.sh` version and platform checksums |
| Buf, sqlc, gosec, govulncheck | `make install-tools` |
| Zensical | `requirements-docs.txt` |
| Container bases | Dockerfile tag plus digest |
| GitHub Actions | immutable commit SHA |

Renovate may propose upgrades, but a version change must update every runtime,
CI image, checksum/digest, and documentation reference together. Do not replace
an exact input with `latest`, a floating branch, or an unverified download.

`make terraform-check` uses the exact host Terraform version when present and
falls back to the digest-pinned container when the host is missing or stale.

ShellCheck, actionlint, golangci-lint, and Trivy use the same rule: the scripts
accept the reviewed host version and otherwise fall back to the digest-pinned
container. If neither the exact host tool nor Docker is available, the quality
gate fails instead of silently running a different release.

`make install-shell-tools` installs the reviewed ripgrep release under
`.tools/bin` after verifying the platform-specific archive checksum. The
toolchain bootstrap contract itself uses only standard shell utilities, so it
can verify the repository before any optional developer tool is installed.

Zensical and every transitive Python package are version- and artifact-hash
locked for the digest-pinned Python 3.13 docs image. `make install-doc-tools`
builds that image; the docs script does not perform an unreviewed runtime
package resolution fallback.

Segmentor runtime dependencies are generated from
`config/segmentor-requirements.in`; both the resulting runtime lock and the
`pip-tools` resolver environment are exact wheel-hash locks. Regeneration runs
inside the same digest-pinned Python image used by the segmentor runtime.

Repository automation is implemented in Go or Bash. Shell and workflow files
may invoke the reviewed Kraken, pip, or Zensical command-line tools, but may not
embed Python through `-c`, standard input, or heredocs. Node tooling is confined
to the `web` and `mirador-scribe` frontend packages.
