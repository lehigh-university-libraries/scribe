#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin"

cat >"$TEST_DIR/bin/buf" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "--version" ]; then
  printf '%s\n' "${FAKE_BUF_VERSION}"
  exit 0
fi
printf 'buf %s\n' "$*" >>"${FAKE_TOOL_LOG}"
EOF

cat >"$TEST_DIR/bin/sqlc" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "version" ]; then
  printf '%s\n' "${FAKE_SQLC_VERSION}"
  exit 0
fi
printf 'sqlc %s\n' "$*" >>"${FAKE_TOOL_LOG}"
EOF

cat >"$TEST_DIR/bin/zensical" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "--version" ]; then
  printf 'zensical, version %s\n' "${FAKE_ZENSICAL_VERSION}"
  exit 0
fi
printf 'zensical %s\n' "$*" >>"${FAKE_TOOL_LOG}"
EOF

cat >"$TEST_DIR/bin/gosec" <<'EOF'
#!/usr/bin/env bash
echo "wrong-version gosec executed" >&2
exit 99
EOF

cat >"$TEST_DIR/bin/govulncheck" <<'EOF'
#!/usr/bin/env bash
echo "govulncheck should not execute after a version failure" >&2
exit 99
EOF

cat >"$TEST_DIR/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "version" ] && [ "${2:-}" = "-m" ]; then
  case "${3##*/}" in
    gosec) printf '\tmod\tgithub.com/securego/gosec/v2\t%s\th1:fixture=\n' "${FAKE_GOSEC_VERSION}" ;;
    govulncheck) printf '\tmod\tgolang.org/x/vuln\t%s\th1:fixture=\n' "${FAKE_GOVULNCHECK_VERSION}" ;;
    *) exit 2 ;;
  esac
  exit 0
fi
echo "unexpected fake go invocation: $*" >&2
exit 2
EOF

chmod +x "$TEST_DIR/bin/"*
tool_log="$TEST_DIR/tools.log"

mkdir -p "$TEST_DIR/yq-bin" "$TEST_DIR/yq-path"
cat >"$TEST_DIR/yq-bin/yq" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'yq (https://github.com/mikefarah/yq/) version %s\n' "${FAKE_YQ_VERSION}"
EOF
cat >"$TEST_DIR/yq-path/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'curl invoked\n' >>"${FAKE_YQ_CURL_LOG}"
exit 73
EOF
chmod +x "$TEST_DIR/yq-bin/yq" "$TEST_DIR/yq-path/curl"
yq_curl_log="$TEST_DIR/yq-curl.log"
TOOLS_BIN="$TEST_DIR/yq-bin" PATH="$TEST_DIR/yq-path:/usr/bin:/bin" \
  FAKE_YQ_VERSION=v4.53.3 FAKE_YQ_CURL_LOG="$yq_curl_log" \
  "$ROOT_DIR/ci/install-yq.sh"
[ ! -e "$yq_curl_log" ]
if TOOLS_BIN="$TEST_DIR/yq-bin" PATH="$TEST_DIR/yq-path:/usr/bin:/bin" \
  FAKE_YQ_VERSION=v4.53.30 FAKE_YQ_CURL_LOG="$yq_curl_log" \
  "$ROOT_DIR/ci/install-yq.sh" >/dev/null 2>&1; then
  echo "yq installer accepted a version with the reviewed version as a prefix" >&2
  exit 1
fi
grep -Fx 'curl invoked' "$yq_curl_log" >/dev/null

if BUF_BIN="$TEST_DIR/bin/buf" FAKE_BUF_VERSION=1.71.0 FAKE_TOOL_LOG="$tool_log" \
  "$ROOT_DIR/ci/proto.sh" >/dev/null 2>"$TEST_DIR/buf.err"; then
  echo "proto generation accepted an unreviewed Buf version" >&2
  exit 1
fi
grep -F 'buf 1.72.0 is required' "$TEST_DIR/buf.err" >/dev/null
BUF_BIN="$TEST_DIR/bin/buf" FAKE_BUF_VERSION=1.72.0 FAKE_TOOL_LOG="$tool_log" \
  "$ROOT_DIR/ci/proto.sh" >/dev/null
grep -F 'buf build .' "$tool_log" >/dev/null
grep -F 'buf generate' "$tool_log" >/dev/null

if SQLC_BIN="$TEST_DIR/bin/sqlc" FAKE_SQLC_VERSION=v1.30.0 FAKE_TOOL_LOG="$tool_log" \
  "$ROOT_DIR/ci/sqlc.sh" >/dev/null 2>"$TEST_DIR/sqlc.err"; then
  echo "sqlc generation accepted an unreviewed sqlc version" >&2
  exit 1
fi
grep -F 'sqlc v1.31.1 is required' "$TEST_DIR/sqlc.err" >/dev/null
SQLC_BIN="$TEST_DIR/bin/sqlc" FAKE_SQLC_VERSION=v1.31.1 FAKE_TOOL_LOG="$tool_log" \
  "$ROOT_DIR/ci/sqlc.sh" >/dev/null
grep -F 'sqlc generate' "$tool_log" >/dev/null

if PATH="$TEST_DIR/bin:$PATH" FAKE_ZENSICAL_VERSION=0.0.50 FAKE_TOOL_LOG="$tool_log" \
  "$ROOT_DIR/ci/docs.sh" build >/dev/null 2>"$TEST_DIR/zensical.err"; then
  echo "documentation build accepted an unreviewed Zensical version" >&2
  exit 1
fi
grep -F 'Zensical 0.0.58 is required' "$TEST_DIR/zensical.err" >/dev/null
PATH="$TEST_DIR/bin:$PATH" FAKE_ZENSICAL_VERSION=0.0.58 FAKE_TOOL_LOG="$tool_log" \
  "$ROOT_DIR/ci/docs.sh" build >/dev/null
grep -F 'zensical build --clean' "$tool_log" >/dev/null

if PATH="$TEST_DIR/bin:/usr/bin:/bin" FAKE_GOSEC_VERSION=v2.27.0 FAKE_GOVULNCHECK_VERSION=v1.6.0 \
  "$ROOT_DIR/ci/security.sh" \
  >/dev/null 2>"$TEST_DIR/security.err"; then
  echo "security scan accepted an unreviewed gosec module" >&2
  exit 1
fi
grep -F 'gosec v2.28.0 is required' "$TEST_DIR/security.err" >/dev/null

if PATH="$TEST_DIR/bin:/usr/bin:/bin" FAKE_GOSEC_VERSION=v2.28.0 FAKE_GOVULNCHECK_VERSION=v1.5.4 \
  "$ROOT_DIR/ci/security.sh" \
  >/dev/null 2>"$TEST_DIR/govulncheck.err"; then
  echo "security scan accepted an unreviewed govulncheck module" >&2
  exit 1
fi
grep -F 'govulncheck v1.6.0 is required' "$TEST_DIR/govulncheck.err" >/dev/null

echo "Codegen, docs, and scanner version rejection behavior passed."
