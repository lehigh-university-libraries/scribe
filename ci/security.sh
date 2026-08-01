#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly EXPECTED_GOSEC_MODULE="github.com/securego/gosec/v2"
readonly EXPECTED_GOSEC_VERSION="v2.28.0"
readonly EXPECTED_GOVULNCHECK_MODULE="golang.org/x/vuln"
readonly EXPECTED_GOVULNCHECK_VERSION="v1.6.0"

if ! command -v go >/dev/null 2>&1 && [ -x /usr/local/go/bin/go ]; then
  export PATH="/usr/local/go/bin:${PATH}"
fi
if ! command -v go >/dev/null 2>&1; then
  echo "Error: Go $(tr -d '\n' < "${ROOT_DIR}/.go-version") is required. Run 'make install-tools' on a Go-enabled host or use CI." >&2
  exit 127
fi

resolve_tool() {
  local name="$1"
  if [ -x "${ROOT_DIR}/.tools/bin/${name}" ]; then
    printf '%s\n' "${ROOT_DIR}/.tools/bin/${name}"
    return
  fi
  if command -v "${name}" >/dev/null 2>&1; then
    command -v "${name}"
    return
  fi
  echo "Error: ${name} is required. Run 'make install-tools'." >&2
  return 127
}

gosec_bin="$(resolve_tool gosec)"
govulncheck_bin="$(resolve_tool govulncheck)"

assert_go_tool_version() {
  local name="$1" binary="$2" module="$3" expected="$4" metadata actual
  metadata="$(go version -m "$binary" 2>/dev/null || true)"
  actual="$(awk -v module="$module" '$1 == "mod" && $2 == module { print $3 }' <<<"$metadata")"
  if [ "$actual" != "$expected" ]; then
    echo "Error: ${name} ${expected} is required; found '${actual:-unknown}'. Run 'make install-security-tools'." >&2
    return 127
  fi
}

assert_go_tool_version gosec "$gosec_bin" "$EXPECTED_GOSEC_MODULE" "$EXPECTED_GOSEC_VERSION"
assert_go_tool_version govulncheck "$govulncheck_bin" "$EXPECTED_GOVULNCHECK_MODULE" "$EXPECTED_GOVULNCHECK_VERSION"

cd "${ROOT_DIR}"
GOFLAGS="${GOFLAGS:+${GOFLAGS} }-buildvcs=false" "${gosec_bin}" \
  -exclude-generated \
  -exclude-dir=.terraform \
  -exclude-dir=node_modules \
  -exclude-dir=site \
  -severity medium \
  -confidence medium \
  ./...
# govulncheck loads the whole program graph. Bound its heap and package build
# parallelism so the release gate also fits on resource-constrained runners.
GOMEMLIMIT="${GOMEMLIMIT:-1024MiB}" \
  GOFLAGS="${GOFLAGS:+${GOFLAGS} }-buildvcs=false -p=1" \
  "${govulncheck_bin}" ./...

npm --prefix web audit --audit-level=moderate
npm --prefix mirador-scribe audit --audit-level=moderate
