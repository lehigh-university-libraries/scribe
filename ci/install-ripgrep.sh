#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS_BIN="${TOOLS_BIN:-${ROOT_DIR}/.tools/bin}"

# renovate: datasource=github-releases depName=BurntSushi/ripgrep
readonly RIPGREP_VERSION="15.2.0"

fail() {
  echo "ripgrep installation failed: $*" >&2
  exit 1
}

destination="${TOOLS_BIN}/rg"
if [ -x "${destination}" ] &&
  [ "$("${destination}" --version | awk 'NR == 1 { print $2 }')" = "${RIPGREP_VERSION}" ]; then
  exit 0
fi

case "$(uname -s):$(uname -m)" in
  Linux:x86_64)
    target="x86_64-unknown-linux-musl"
    checksum="33e15bcf1624b25cdd2a55813a47a2f95dbe126268203e76aa6a585d1e7b149c"
    ;;
  Linux:aarch64|Linux:arm64)
    target="aarch64-unknown-linux-musl"
    checksum="800b1e7206afe799dfb5a6901f23147cfaabe0e52210538100f61e86e1740915"
    ;;
  Darwin:x86_64)
    target="x86_64-apple-darwin"
    checksum="af7825fcc69a2afc7a7aea55fc9af90e26421d8f20fe59df32e233c0b8a231c1"
    ;;
  Darwin:arm64|Darwin:aarch64)
    target="aarch64-apple-darwin"
    checksum="3750b2e93f37e0c692657da574d7019a101c0084da05a790c83fd335bad973e4"
    ;;
  *)
    fail "unsupported platform $(uname -s)/$(uname -m); install ripgrep ${RIPGREP_VERSION} and place rg in ${TOOLS_BIN}"
    ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

temporary_parent="${TMPDIR:-${ROOT_DIR}/.tools/tmp}"
mkdir -p "${temporary_parent}"
temporary_dir="$(mktemp -d "${temporary_parent%/}/install-ripgrep.XXXXXXXXXX")"
destination_tmp=""
cleanup() {
  if [ -n "${destination_tmp}" ]; then
    unlink "${destination_tmp}" 2>/dev/null || true
  fi
  rm -rf "${temporary_dir}"
}
trap cleanup EXIT
archive_name="ripgrep-${RIPGREP_VERSION}-${target}.tar.gz"
archive_path="${temporary_dir}/${archive_name}"
download_url="https://github.com/BurntSushi/ripgrep/releases/download/${RIPGREP_VERSION}/${archive_name}"

curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error --retry 3 \
  --output "${archive_path}" "${download_url}"

if command -v sha256sum >/dev/null 2>&1; then
  printf '%s  %s\n' "${checksum}" "${archive_path}" | sha256sum --check --status - ||
    fail "checksum verification failed for ${archive_name}"
elif command -v shasum >/dev/null 2>&1; then
  actual_checksum="$(shasum -a 256 "${archive_path}" | awk '{ print $1 }')"
  [ "${actual_checksum}" = "${checksum}" ] || fail "checksum verification failed for ${archive_name}"
else
  fail "sha256sum or shasum is required"
fi

tar -xzf "${archive_path}" -C "${temporary_dir}"
source_binary="${temporary_dir}/ripgrep-${RIPGREP_VERSION}-${target}/rg"
[ -x "${source_binary}" ] || fail "release archive did not contain rg"

mkdir -p "${TOOLS_BIN}"
destination_tmp="${destination}.tmp.$$"
cp "${source_binary}" "${destination_tmp}"
[ -x "${destination_tmp}" ] || fail "copied rg binary is not executable"
mv "${destination_tmp}" "${destination}"
destination_tmp=""

echo "Installed ripgrep ${RIPGREP_VERSION} in ${TOOLS_BIN}." >&2
