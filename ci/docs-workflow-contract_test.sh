#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "docs workflow contract failed: $*" >&2
  exit 1
}

require_pattern() {
  local pattern="$1"
  local file="$2"
  rg -q -- "$pattern" "$file" || fail "$file is missing required pattern: $pattern"
}

forbid_pattern() {
  local pattern="$1"
  local file="$2"
  if rg -q -- "$pattern" "$file"; then
    fail "$file contains forbidden pattern: $pattern"
  fi
}

require_pattern '^docs-build: install-doc-tools ## ' Makefile
require_pattern '^docs: docs-build ## ' Makefile
require_pattern '^docs-serve: install-doc-tools ## ' Makefile
require_pattern '^[[:space:]]*@SCRIBE_DOCS_FORCE_DOCKER=true \./ci/docs\.sh build$' Makefile
require_pattern '^[[:space:]]+run_make docs-build$' ci/run-ci.sh
forbid_pattern '^[[:space:]]+run_make docs$' ci/run-ci.sh

require_pattern 'run: \./ci/run-ci\.sh infrastructure' .github/workflows/lint-test.yaml
[ "$(rg -c '^[[:space:]]*run: make docs-build$' .github/workflows/docs.yml)" -eq 1 ] ||
  fail "Pages must call make docs-build exactly once"
forbid_pattern 'make install-doc-tools docs' .github/workflows/lint-test.yaml

require_pattern '^permissions:$' .github/workflows/docs.yml
require_pattern '^  contents: read$' .github/workflows/docs.yml
require_pattern '^  pages: write$' .github/workflows/docs.yml
require_pattern '^  id-token: write$' .github/workflows/docs.yml
require_pattern '^      - main$' .github/workflows/docs.yml
require_pattern '^concurrency:$' .github/workflows/docs.yml
require_pattern '^  group: pages$' .github/workflows/docs.yml
require_pattern '^  cancel-in-progress: false$' .github/workflows/docs.yml
require_pattern '^    environment:$' .github/workflows/docs.yml
require_pattern '^      name: github-pages$' .github/workflows/docs.yml
require_pattern '^[[:space:]]+url: \$\{\{ steps\.deployment\.outputs\.page_url \}\}$' .github/workflows/docs.yml
require_pattern 'actions/checkout@[0-9a-f]{40}' .github/workflows/docs.yml
require_pattern '^[[:space:]]+persist-credentials: false$' .github/workflows/docs.yml
require_pattern 'actions/configure-pages@[0-9a-f]{40}' .github/workflows/docs.yml
require_pattern 'actions/upload-pages-artifact@[0-9a-f]{40}' .github/workflows/docs.yml
require_pattern 'actions/deploy-pages@[0-9a-f]{40}' .github/workflows/docs.yml
require_pattern '^[[:space:]]+path: \./site$' .github/workflows/docs.yml

require_pattern '^site_url = "https://lehigh-university-libraries\.github\.io/scribe/"$' zensical.toml
require_pattern '^repo_url = "https://github\.com/lehigh-university-libraries/scribe"$' zensical.toml
require_pattern '^edit_uri = "edit/main/docs/"$' zensical.toml
require_pattern '^site_dir = "site"$' zensical.toml
forbid_pattern '^site_dir = "docs/site"$' zensical.toml
require_pattern 'docker_args\+=\("--clean" "--strict"\)' ci/docs.sh
require_pattern 'build --clean --strict' ci/docs.sh
require_pattern '^prepare_site_output\(\) \{$' ci/docs.sh
require_pattern 'find "\$\{site_dir\}" -mindepth 1 -depth -delete' ci/docs.sh
require_pattern 'documentation output must be a real directory' ci/docs.sh
require_pattern "docker image inspect --format '\\{\\{\\.Id\\}\\}'" ci/docs.sh
require_pattern '^  for attempt in 1 2 3 4 5; do$' ci/docs.sh
require_pattern 'site/\.scribe-docs-bind-probe' ci/docs.sh
require_pattern '^FROM python:[^@[:space:]]+@sha256:[0-9a-f]{64}$' Dockerfile.docs
require_pattern 'pip install .*--require-hashes' Dockerfile.docs
require_pattern '^zensical==0\.0\.51' requirements-docs.txt

for page in \
  docs/getting-started/using-contexts.md \
  docs/development/adding-transcription-model.md \
  docs/development/adding-segmentation-model.md \
  docs/development/adding-system-context.md \
  docs/development/documentation.md \
  docs/operations/troubleshooting.md \
  docs/reference/release-criteria.md \
  docs/reference/engineering-contract.md; do
  [ -s "$page" ] || fail "$page must exist and be non-empty"
  require_pattern "\"${page#docs/}\"" zensical.toml
done

require_pattern 'docs/reference/release-criteria\.md' AGENTS.md
require_pattern 'docs/reference/engineering-contract\.md' AGENTS.md
require_pattern 'docs/reference/quality-gates\.md' AGENTS.md
require_pattern 'docs/development/documentation\.md' AGENTS.md

require_pattern 'sudo systemctl restart cloud-compose\.service' docs/operations/troubleshooting.md
require_pattern 'sudo systemctl restart cloud-compose-bootstrap\.service' docs/operations/troubleshooting.md
require_pattern 'sudo journalctl -b' docs/operations/troubleshooting.md
require_pattern '-u cloud-compose-bootstrap\.service' docs/operations/troubleshooting.md
require_pattern 'run-cloud-run-readiness\.sh' docs/operations/troubleshooting.md
require_pattern "Do not run \`git pull\`" docs/operations/troubleshooting.md
require_pattern '/mnt/disks/data/scribe/prod' docs/operations/troubleshooting.md

echo "Docs workflow contract passed."
