#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
resolver="${repo_root}/ci/resolve-gar-image.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "GAR image resolver contract failed: $*" >&2
  exit 1
}

fake_bin="${tmp}/bin"
mkdir -p "$fake_bin"
cat >"${fake_bin}/gcloud" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

{
  printf 'call'
  printf '\t%s' "$@"
  printf '\n'
} >>"$GCLOUD_LOG"

if [ "${GCLOUD_EXIT:-0}" -ne 0 ]; then
  echo "mock Artifact Registry failure" >&2
  exit "$GCLOUD_EXIT"
fi

printf '%s\n' "${GCLOUD_RESPONSE:-[]}"
EOF
chmod +x "${fake_bin}/gcloud"

gcloud_log="${tmp}/gcloud.log"
: >"$gcloud_log"

run_resolver() {
  local response="$1"
  local exit_code="$2"
  local image_ref="$3"

  env \
    PATH="${fake_bin}:${PATH}" \
    GCLOUD_LOG="$gcloud_log" \
    GCLOUD_RESPONSE="$response" \
    GCLOUD_EXIT="$exit_code" \
    "$resolver" "$image_ref"
}

digest_a="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
digest_b="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
image="us-docker.pkg.dev/example-project/internal/scribe-frontend"
tagged_image="${image}:pr-75"
pinned_image="${image}@${digest_a}"

[[ "$(run_resolver 'invalid' 0 "$pinned_image")" == "$pinned_image" ]] ||
  fail "an already pinned image was not returned unchanged"
[[ ! -s "$gcloud_log" ]] ||
  fail "an already pinned image must not call gcloud"

response="$(
  jq -cn \
    --arg image "$image" \
    --arg digest "$digest_a" \
    '[{
      image: $image,
      tag: "projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/tags/pr-75",
      version: ("projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/versions/" + $digest)
    }]'
)"
resolved="$(run_resolver "$response" 0 "$tagged_image")"
[[ "$resolved" == "${image}@${digest_a}" ]] ||
  fail "the exact GAR tag did not resolve to its immutable digest"
expected_call=$'call\tartifacts\tdocker\ttags\tlist\t'"${image}"$'\t--format=json(image,tag,version)'
[[ "$(<"$gcloud_log")" == "$expected_call" ]] ||
  fail "the resolver did not use the repository-scoped tag listing API"

: >"$gcloud_log"
nested_image="us-docker.pkg.dev/example-project/internal/team/scribe-frontend"
nested_response="$(
  jq -cn \
    --arg image "$nested_image" \
    --arg digest "$digest_a" \
    '[{
      image: $image,
      tag: "projects/example-project/locations/us/repositories/internal/packages/team%2Fscribe-frontend/tags/main",
      version: ("projects/example-project/locations/us/repositories/internal/packages/team%2Fscribe-frontend/versions/" + $digest)
    }]'
)"
[[ "$(
  run_resolver "$nested_response" 0 "${nested_image}:main"
)" == "${nested_image}@${digest_a}" ]] ||
  fail "a nested GAR image path did not resolve"

unrelated_response="$(
  jq -cn \
    --arg image "$image" \
    --arg digest "$digest_a" \
    '[{
      image: $image,
      tag: "projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/tags/pr-750",
      version: ("projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/versions/" + $digest)
    }]'
)"
if run_resolver "$unrelated_response" 0 "$tagged_image" \
  >"${tmp}/unrelated.stdout" 2>"${tmp}/unrelated.stderr"; then
  fail "a different tag was accepted"
fi

duplicate_response="$(
  jq -cn \
    --arg image "$image" \
    --arg digest_a "$digest_a" \
    --arg digest_b "$digest_b" \
    '[
      {
        image: $image,
        tag: "projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/tags/pr-75",
        version: ("projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/versions/" + $digest_a)
      },
      {
        image: $image,
        tag: "projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/tags/pr-75",
        version: ("projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/versions/" + $digest_b)
      }
    ]'
)"
if run_resolver "$duplicate_response" 0 "$tagged_image" \
  >"${tmp}/duplicate.stdout" 2>"${tmp}/duplicate.stderr"; then
  fail "conflicting tag records were accepted"
fi

malformed_version_response="$(
  jq -cn \
    --arg image "$image" \
    '[{
      image: $image,
      tag: "projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/tags/pr-75",
      version: "projects/example-project/locations/us/repositories/internal/packages/scribe-frontend/versions/latest"
    }]'
)"
for name_and_response in \
  "missing:[]" \
  "malformed-version:${malformed_version_response}" \
  "malformed-json:not-json"; do
  name="${name_and_response%%:*}"
  invalid_response="${name_and_response#*:}"
  if run_resolver "$invalid_response" 0 "$tagged_image" \
    >"${tmp}/${name}.stdout" 2>"${tmp}/${name}.stderr"; then
    fail "${name} response was accepted"
  fi
  grep -Fq "failed to resolve digest for Artifact Registry image" \
    "${tmp}/${name}.stderr" ||
    fail "${name} response did not produce the bounded resolver error"
  if grep -Fq -- "$invalid_response" "${tmp}/${name}.stderr"; then
    fail "${name} response contents leaked to stderr"
  fi
done

if run_resolver '[]' 23 "$tagged_image" \
  >"${tmp}/gcloud-failure.stdout" 2>"${tmp}/gcloud-failure.stderr"; then
  fail "a gcloud failure was ignored"
fi
grep -Fq "mock Artifact Registry failure" "${tmp}/gcloud-failure.stderr" ||
  fail "gcloud diagnostics were not preserved"

: >"$gcloud_log"
if run_resolver "$response" 0 \
  "us-docker.pkg.dev/example-project/internal/scribe-frontend@latest" \
  >"${tmp}/invalid-ref.stdout" 2>"${tmp}/invalid-ref.stderr"; then
  fail "an invalid digest reference was accepted"
fi
[[ ! -s "$gcloud_log" ]] ||
  fail "an invalid image reference must fail before gcloud runs"

if rg -q 'artifacts docker images describe|containeranalysis' "$resolver"; then
  fail "the resolver must not require Container Analysis metadata access"
fi

echo "GAR image resolver contracts passed."
