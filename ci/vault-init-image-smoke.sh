#!/usr/bin/env bash

set -euo pipefail

image="${1:-${VAULT_INIT_IMAGE:-}}"
if [[ ! "$image" =~ @sha256:[0-9a-f]{64}$ ]] && [[ ! "$image" =~ ^scribe-vault-init:[A-Za-z0-9._-]+$ ]]; then
  echo "usage: $0 <digest-pinned-image>; local scribe-vault-init:<tag> is also accepted" >&2
  exit 2
fi
command -v docker >/dev/null 2>&1 || {
  echo "docker is required for the Vault init runtime image smoke test" >&2
  exit 127
}

docker run --rm \
  --read-only \
  --cap-drop=ALL \
  --security-opt no-new-privileges \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m,mode=1777 \
  --entrypoint /bin/sh \
  "$image" -eu -c '
    test -x /usr/local/bin/vault-init.sh
    test -r /usr/local/lib/scribe/vault-retry.sh
    command -v curl >/dev/null
    command -v jq >/dev/null
    command -v openssl >/dev/null
    ! grep -Eq "apk (add|update)" /usr/local/bin/vault-init.sh
  '

echo "Vault init runtime image smoke passed for $image."
