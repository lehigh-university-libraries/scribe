#!/usr/bin/env bash

set -euo pipefail

repository="${1:?repository path is required}"
previous_sha="${2:?previous production SHA is required}"
current_sha="${3:?current production SHA is required}"

die() {
  echo "production source lineage verification failed: $*" >&2
  exit 1
}

ensure_commit() {
  local sha="$1"
  local label="$2"
  if ! git -C "$repository" cat-file -e "${sha}^{commit}" 2>/dev/null; then
    git -C "$repository" fetch --quiet --no-tags origin "$sha" 2>/dev/null ||
      die "$label commit is unavailable"
    git -C "$repository" cat-file -e "${sha}^{commit}" 2>/dev/null ||
      die "fetched $label object is not a commit"
  fi
}

for sha in "$previous_sha" "$current_sha"; do
  [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || die "commit SHAs must be lowercase 40-character hex"
done
ensure_commit "$previous_sha" "previous production"
ensure_commit "$current_sha" "current production"

if git -C "$repository" merge-base --is-ancestor "$previous_sha" "$current_sha"; then
  exit 0
fi

event_name="${DEPLOY_EVENT_NAME:-}"
event_forced="${DEPLOY_EVENT_FORCED:-}"
event_before="${DEPLOY_EVENT_BEFORE:-}"
event_after="${DEPLOY_EVENT_AFTER:-}"

[ "$event_name" = "push" ] || die "non-ancestor deployment is not a push retry"
[ "$event_forced" = "true" ] || die "non-ancestor deployment was not force-pushed"
[ "$event_after" = "$current_sha" ] || die "push after SHA does not match the reviewed deployment"
[[ "$event_before" =~ ^[0-9a-f]{40}$ ]] || die "push before SHA is invalid"
ensure_commit "$event_before" "push before"

common_parent=""
common_subject=""
for sha in "$previous_sha" "$event_before" "$current_sha"; do
  read -r -a ancestry <<<"$(git -C "$repository" rev-list --parents -n 1 "$sha")"
  [ "${#ancestry[@]}" -eq 2 ] || die "amended deployment commits must have exactly one parent"
  parent="${ancestry[1]}"
  subject="$(git -C "$repository" show -s --format=%s "$sha")"
  if [ -z "$common_parent" ]; then
    common_parent="$parent"
    common_subject="$subject"
    continue
  fi
  [ "$parent" = "$common_parent" ] || die "amended deployment commits do not occupy the same commit slot"
  [ "$subject" = "$common_subject" ] || die "amended deployment commits do not retain the reviewed subject"
done
