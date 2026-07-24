#!/usr/bin/env bash

set -euo pipefail

fail() {
  echo "preview image publication failed: $*" >&2
  exit 1
}

: "${BACKEND_TAG:?BACKEND_TAG is required}"
: "${FRONTEND_TAG:?FRONTEND_TAG is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"
: "${HOME:?HOME is required}"
: "${RUNNER_TEMP:?RUNNER_TEMP is required}"
: "${SKOPEO_IMAGE:?SKOPEO_IMAGE is required}"

[[ "$BACKEND_TAG" =~ ^ghcr\.io/lehigh-university-libraries/scribe:(pr-[1-9][0-9]*)$ ]] ||
  fail "BACKEND_TAG must be the Scribe preview GHCR tag"
preview_tag="${BASH_REMATCH[1]}"
[[ "$FRONTEND_TAG" == "ghcr.io/lehigh-university-libraries/scribe-frontend:${preview_tag}" ]] ||
  fail "FRONTEND_TAG must be the matching Scribe frontend preview GHCR tag"
[[ "$SKOPEO_IMAGE" =~ ^quay\.io/skopeo/stable:v[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$ ]] ||
  fail "SKOPEO_IMAGE must be a digest-pinned Skopeo stable image"

[[ "$RUNNER_TEMP" == /* && -d "$RUNNER_TEMP" && ! -L "$RUNNER_TEMP" ]] ||
  fail "RUNNER_TEMP must be an absolute, non-symlink directory"
runner_temp="$(realpath -e -- "$RUNNER_TEMP")" ||
  fail "RUNNER_TEMP cannot be resolved"
[[ "$runner_temp" != "/" ]] || fail "RUNNER_TEMP cannot be the filesystem root"

image_dir="${runner_temp}/preview-images"
[[ -d "$image_dir" && ! -L "$image_dir" ]] ||
  fail "preview image directory must be a non-symlink directory"
[[ "$(realpath -e -- "$image_dir")" == "$image_dir" ]] ||
  fail "preview image directory must resolve inside RUNNER_TEMP"

validate_archive() {
  local archive="$1"
  local resolved

  [[ "$archive" == "${image_dir}/"* && -f "$archive" && -s "$archive" && ! -L "$archive" ]] ||
    fail "preview OCI archive is missing, empty, or not a regular file: ${archive##*/}"
  resolved="$(realpath -e -- "$archive")" ||
    fail "preview OCI archive cannot be resolved: ${archive##*/}"
  [[ "${resolved%/*}" == "$image_dir" && "$resolved" == "$archive" ]] ||
    fail "preview OCI archive must resolve directly inside the preview image directory"
}

backend_archive="${image_dir}/scribe-backend.oci.tar"
frontend_archive="${image_dir}/scribe-frontend.oci.tar"
validate_archive "$backend_archive"
validate_archive "$frontend_archive"

[[ "$HOME" == /* && -d "$HOME" ]] || fail "HOME must be an absolute directory"
auth_file="${HOME}/.docker/config.json"
[[ -f "$auth_file" && -s "$auth_file" && ! -L "$auth_file" ]] ||
  fail "Docker authentication file is missing, empty, or not a regular file"
auth_file="$(realpath -e -- "$auth_file")" ||
  fail "Docker authentication file cannot be resolved"

[[ "$GITHUB_OUTPUT" == /* && -f "$GITHUB_OUTPUT" && ! -L "$GITHUB_OUTPUT" && -w "$GITHUB_OUTPUT" ]] ||
  fail "GITHUB_OUTPUT must be a writable, non-symlink regular file"
output_file="$(realpath -e -- "$GITHUB_OUTPUT")" ||
  fail "GITHUB_OUTPUT cannot be resolved"
[[ "$output_file" == "${runner_temp}/"* ]] ||
  fail "GITHUB_OUTPUT must resolve inside RUNNER_TEMP"

command -v docker >/dev/null 2>&1 || fail "docker is required"

runtime_uid="$(id -u)"
runtime_gid="$(id -g)"
[[ "$runtime_uid" =~ ^[0-9]+$ && "$runtime_gid" =~ ^[0-9]+$ ]] ||
  fail "runtime UID and GID must be numeric"

umask 077
work_dir=""
cleanup() {
  local status=$?
  local cleanup_status=0
  local candidate="${work_dir:-}"

  trap - EXIT
  if [[ -n "$candidate" ]]; then
    if [[ "${candidate%/*}" != "$runner_temp" ||
      ! "${candidate##*/}" =~ ^scribe-preview-publish\.[A-Za-z0-9]{10}$ ||
      ! -d "$candidate" || -L "$candidate" ]]; then
      echo "preview image publication cleanup refused an unexpected path" >&2
      cleanup_status=1
    elif ! rm -rf --one-file-system -- "$candidate"; then
      echo "preview image publication could not remove its private temporary directory" >&2
      cleanup_status=1
    fi
  fi
  if ((status == 0 && cleanup_status != 0)); then
    status="$cleanup_status"
  fi
  exit "$status"
}

work_dir="$(mktemp -d -- "${runner_temp}/scribe-preview-publish.XXXXXXXXXX")" ||
  fail "could not create private Skopeo temporary directory"
trap cleanup EXIT
[[ "${work_dir%/*}" == "$runner_temp" && -d "$work_dir" && ! -L "$work_dir" ]] ||
  fail "mktemp returned an unexpected path"
chmod 0700 -- "$work_dir"

skopeo=(
  docker run --rm
  --read-only
  --cap-drop=ALL
  --security-opt no-new-privileges
  --user "${runtime_uid}:${runtime_gid}"
  --env TMPDIR=/var/tmp
  --tmpfs "/tmp:rw,nosuid,nodev,noexec,size=64m"
  --volume "${work_dir}:/var/tmp:rw"
  --volume "${image_dir}:/images:ro"
  --volume "${auth_file}:/auth/config.json:ro"
  "$SKOPEO_IMAGE"
)

"${skopeo[@]}" copy --all --authfile /auth/config.json \
  oci-archive:/images/scribe-backend.oci.tar "docker://${BACKEND_TAG}"
"${skopeo[@]}" copy --all --authfile /auth/config.json \
  oci-archive:/images/scribe-frontend.oci.tar "docker://${FRONTEND_TAG}"

backend_digest="$("${skopeo[@]}" inspect --authfile /auth/config.json \
  --format '{{.Digest}}' "docker://${BACKEND_TAG}")"
frontend_digest="$("${skopeo[@]}" inspect --authfile /auth/config.json \
  --format '{{.Digest}}' "docker://${FRONTEND_TAG}")"
[[ "$backend_digest" =~ ^sha256:[0-9a-f]{64}$ ]] ||
  fail "Skopeo returned an invalid backend digest"
[[ "$frontend_digest" =~ ^sha256:[0-9a-f]{64}$ ]] ||
  fail "Skopeo returned an invalid frontend digest"

{
  printf 'backend_image=%s@%s\n' "${BACKEND_TAG%:*}" "$backend_digest"
  printf 'frontend_image=%s@%s\n' "${FRONTEND_TAG%:*}" "$frontend_digest"
} >>"$output_file"
