#!/usr/bin/env bash

set -euo pipefail

required=(docker git jq)
missing=()
for tool in "${required[@]}"; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    missing+=("${tool}")
  fi
done

if ! docker compose version >/dev/null 2>&1; then
  missing+=("docker-compose-plugin")
fi

if ((${#missing[@]} > 0)); then
  echo "Missing required local-development tools: ${missing[*]}" >&2
  exit 1
fi

echo "Docker: $(docker --version)"
echo "Compose: $(docker compose version --short)"
echo "Git: $(git --version)"
go_bin="$(command -v go || true)"
if [ -z "${go_bin}" ] && [ -x /usr/local/go/bin/go ]; then
  go_bin=/usr/local/go/bin/go
fi
if [ -n "${go_bin}" ]; then
  echo "Go: $("${go_bin}" version) (repository target: $(tr -d '\n' < .go-version))"
else
  echo "Go: not installed (containerized build/test targets still work)"
fi
if command -v node >/dev/null 2>&1; then
  echo "Node: $(node --version) (repository target: $(tr -d '\n' < .nvmrc))"
else
  echo "Node: not installed (containerized frontend checks still work)"
fi

"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/toolchain-check.sh" --host
