#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
input="$repo_root/config/segmentor-requirements.in"
lock="$repo_root/config/segmentor-requirements.lock"
dockerfile="$repo_root/Dockerfile.segmentor"
resolver_lock="$repo_root/config/pip-tools-requirements.lock"

[[ -s "$input" && -s "$lock" ]] || {
  echo "Segmentor input and generated lock must both be committed." >&2
  exit 1
}
[[ -s "$resolver_lock" ]] || {
  echo "The hash-locked pip-tools resolver environment must be committed." >&2
  exit 1
}

awk '
  function finish() {
    if (package != "" && hashes == 0) {
      printf "hash-locked artifact missing for %s\n", package > "/dev/stderr"
      exit 1
    }
  }
  /^[A-Za-z0-9_.-]+==[^[:space:]]+[[:space:]]*\\$/ {
    finish()
    package = $1
    hashes = 0
    next
  }
  /--hash=sha256:[0-9a-f]{64}/ { hashes++ }
  END { finish() }
' "$lock"

if awk '/^[A-Za-z0-9_.-]/ && $0 !~ /^--/ && $0 !~ /^[A-Za-z0-9_.-]+==[^[:space:]]+[[:space:]]*\\$/ { bad = 1 } END { exit !bad }' "$lock"; then
  echo "Segmentor lock contains a non-exact package requirement." >&2
  exit 1
fi
if grep -Eq '^--(index-url|trusted-host|find-links)' "$lock"; then
  echo "Segmentor lock may only add the reviewed PyTorch CPU index." >&2
  exit 1
fi

for requirement in kraken==7.0.2 torch==2.10.0+cpu torchvision==0.25.0+cpu setuptools==; do
  grep -Eq "^${requirement//+/\\+}" "$lock" || {
    echo "Segmentor lock is missing ${requirement}." >&2
    exit 1
  }
done

grep -Fq -- '--require-hashes' "$dockerfile" || {
  echo "Dockerfile.segmentor does not enforce requirement hashes." >&2
  exit 1
}
grep -Fq -- '--only-binary=:all:' "$dockerfile" || {
  echo "Dockerfile.segmentor can build unreviewed source distributions." >&2
  exit 1
}
grep -Eq '^pip-tools==7\.5\.3[[:space:]]*\\$' "$resolver_lock" || {
  echo "Resolver lock is missing the reviewed pip-tools version." >&2
  exit 1
}
if [ "$(grep -Ec -- '--hash=sha256:[0-9a-f]{64}$' "$resolver_lock")" -ne 8 ]; then
  echo "Resolver lock is missing exact wheel hashes." >&2
  exit 1
fi
grep -Fq -- '--require-hashes --only-binary=:all:' "$repo_root/ci/segmentor-lock.sh" || {
  echo "Segmentor lock regeneration does not enforce its resolver hashes." >&2
  exit 1
}

echo "Segmentor Python dependency lock is exact and hash-enforced."
