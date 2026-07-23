#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS_BIN="${TOOLS_BIN:-${ROOT_DIR}/.tools/bin}"

# renovate: datasource=github-releases depName=mikefarah/yq
readonly YQ_VERSION="4.53.3"

fail() {
  echo "yq installation failed: $*" >&2
  exit 1
}

yq_reports_expected_version() {
  local binary="$1"
  local reported

  reported="$("${binary}" --version 2>/dev/null)" || return 1
  [[ "${reported}" =~ ^yq[[:space:]].*[[:space:]]version[[:space:]]v([0-9]+\.[0-9]+\.[0-9]+)$ ]] ||
    return 1
  [ "${BASH_REMATCH[1]}" = "${YQ_VERSION}" ]
}

destination="${TOOLS_BIN}/yq"
if [ -x "${destination}" ] && yq_reports_expected_version "${destination}"; then
  exit 0
fi

case "$(uname -s):$(uname -m)" in
  Linux:x86_64)
    target="linux_amd64"
    checksum="fa52a4e758c63d38299163fbdd1edfb4c4963247918bf9c1c5d31d84789eded4"
    ;;
  Linux:aarch64|Linux:arm64)
    target="linux_arm64"
    checksum="578648e463a11c1b6db6010cbf41eafed6bee79466fcffa1bb446672cf7945ea"
    ;;
  Darwin:x86_64)
    target="darwin_amd64"
    checksum="b4ba1ecce3c47f00803f4f964de38394326c7a32eb6540616e04fb2935a0f08d"
    ;;
  Darwin:arm64|Darwin:aarch64)
    target="darwin_arm64"
    checksum="877de31753a4dd2401aa048937aa9a7fc4d5f6ce858cf31508c5802954297213"
    ;;
  *)
    fail "unsupported platform $(uname -s)/$(uname -m); install yq ${YQ_VERSION} and place yq in ${TOOLS_BIN}"
    ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"

temporary_parent="${TMPDIR:-${ROOT_DIR}/.tools/tmp}"
mkdir -p "${temporary_parent}"
temporary_dir="$(mktemp -d "${temporary_parent%/}/install-yq.XXXXXXXXXX")"
destination_tmp=""
cleanup() {
  if [ -n "${destination_tmp}" ]; then
    unlink "${destination_tmp}" 2>/dev/null || true
  fi
  find "${temporary_dir}" -depth -delete
}
trap cleanup EXIT

binary_name="yq_${target}"
binary_path="${temporary_dir}/${binary_name}"
download_url="https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/${binary_name}"

curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error --retry 3 \
  --output "${binary_path}" "${download_url}"

if command -v sha256sum >/dev/null 2>&1; then
  printf '%s  %s\n' "${checksum}" "${binary_path}" | sha256sum --check --status - ||
    fail "checksum verification failed for ${binary_name}"
elif command -v shasum >/dev/null 2>&1; then
  actual_checksum="$(shasum -a 256 "${binary_path}" | awk '{ print $1 }')"
  [ "${actual_checksum}" = "${checksum}" ] || fail "checksum verification failed for ${binary_name}"
else
  fail "sha256sum or shasum is required"
fi

mkdir -p "${TOOLS_BIN}"
destination_tmp="${destination}.tmp.$$"
cp "${binary_path}" "${destination_tmp}"
chmod 0755 "${destination_tmp}"
yq_reports_expected_version "${destination_tmp}" ||
  fail "downloaded yq reports an unexpected version"
mv "${destination_tmp}" "${destination}"
destination_tmp=""

echo "Installed yq ${YQ_VERSION} in ${TOOLS_BIN}." >&2
