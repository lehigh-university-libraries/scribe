# Pinned toolchain

| Tool | Version source |
| --- | --- |
| Go | `.go-version` |
| Node.js | `.nvmrc` |
| Python (packaged Kraken/Zensical tooling only) | `.tool-versions` and image build arguments |
| Terraform | `.tool-versions` and workflows |
| ripgrep | `ci/install-ripgrep.sh` version and platform checksums |
| yq | `ci/install-yq.sh` version and platform checksums |
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

Workflow linting uses the host only when both actionlint and ShellCheck match
the reviewed versions. Otherwise the actionlint image supplies both pinned
tools, so embedded workflow scripts receive the same checks locally and in CI.
Both paths explicitly load `.github/actionlint.yaml`; they do not depend on
repository metadata being present for configuration discovery.

`make install-shell-tools` installs the reviewed ripgrep and yq releases under
`.tools/bin` after verifying platform-specific checksums. OCR matrix generation
and image resolution depend on this target in both local use and GitHub Actions;
the workflow does not maintain a second tool-download path. The toolchain
bootstrap contract itself uses only standard shell utilities, so it can verify
the repository before any optional developer tool is installed.

Zensical and every transitive Python package are version- and artifact-hash
locked for the digest-pinned Python 3.13 docs image. `make docs-build` builds
that image and runs the strict site build; `make install-doc-tools` builds only
the image. Architecture-specific wheels needed by the supported amd64 and arm64
build hosts are both hash-locked. The docs script does not perform an unreviewed
runtime package resolution fallback.

Segmentor runtime dependencies are generated from
`config/segmentor-requirements.in`; both the resulting runtime lock and the
`pip-tools` resolver environment are exact wheel-hash locks. Regeneration runs
inside the same digest-pinned Python image used by the segmentor runtime.

Repository automation is implemented in Go or Bash. Shell and workflow files
may invoke the reviewed Kraken, pip, or Zensical command-line tools, but may not
embed Python through `-c`, standard input, or heredocs. Node tooling is confined
to the `web` and `mirador-scribe` frontend packages.
