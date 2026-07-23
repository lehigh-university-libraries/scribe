#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Traefik v3 rule contract failed: $*" >&2
  exit 1
}

rg -q 'image: traefik:v3\.' docker-compose.yaml ||
  fail "the Compose edge is not pinned to Traefik v3"
if rg -n 'Method\(`[^`]+`[[:space:]]*,' conf/traefik; then
  fail "Traefik v3 Method matchers accept exactly one method"
fi
rg -Fq \
  'rule: (Method(`GET`) || Method(`HEAD`) || Method(`OPTIONS`)) && (Path(`/iiif`) || PathPrefix(`/iiif/`) || Path(`/presentation`) || PathPrefix(`/presentation/`))' \
  conf/traefik/dynamic/triplet.yml ||
  fail "the public Triplet router does not explicitly allow GET, HEAD, and OPTIONS"

echo "Traefik v3 public Triplet matchers use one argument per Method rule."
