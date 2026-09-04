#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
publisher="${repo_root}/ci/publish-preview-images.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "publish preview images contract failed: $*" >&2
  exit 1
}

fake_bin="${tmp}/bin"
mkdir -p "$fake_bin"
cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

{
  printf 'call'
  printf '\t%s' "$@"
  printf '\n'
} >>"$DOCKER_LOG"

work_dir=""
is_inspect=false
target=""
for argument in "$@"; do
  case "$argument" in
    *:/var/tmp:rw) work_dir="${argument%:/var/tmp:rw}" ;;
    inspect) is_inspect=true ;;
    docker://*) target="$argument" ;;
  esac
done

[[ -n "$work_dir" && -d "$work_dir" ]] || {
  echo "missing private /var/tmp bind mount" >&2
  exit 1
}
printf '%s\t%s\n' "$work_dir" "$(stat -c '%a' "$work_dir")" >>"$DOCKER_OBSERVATIONS"
touch "${work_dir}/mock-skopeo-temporary-file"

if [[ "${DOCKER_FAIL_COPY:-false}" == "true" && "$is_inspect" == "false" ]]; then
  exit 23
fi

if "$is_inspect"; then
  case "$target" in
    docker://ghcr.io/lehigh-university-libraries/scribe:pr-75)
      printf 'sha256:%064d\n' 0
      ;;
    docker://ghcr.io/lehigh-university-libraries/scribe-frontend:pr-75)
      printf 'sha256:%064d\n' 1
      ;;
    *)
      echo "unexpected inspect target: $target" >&2
      exit 1
      ;;
  esac
fi
EOF
chmod +x "${fake_bin}/docker"

setup_case() {
  local name="$1"
  local root="${tmp}/${name}"

  mkdir -p "${root}/runner/preview-images" "${root}/home/.docker"
  printf 'backend archive\n' >"${root}/runner/preview-images/scribe-backend.oci.tar"
  printf 'frontend archive\n' >"${root}/runner/preview-images/scribe-frontend.oci.tar"
  printf '{"auths":{"ghcr.io":{}}}\n' >"${root}/home/.docker/config.json"
  : >"${root}/runner/github-output"
  : >"${root}/docker.log"
  : >"${root}/docker-observations"
}

run_publisher() {
  local root="$1"
  shift

  env \
    PATH="${fake_bin}:${PATH}" \
    HOME="${root}/home" \
    RUNNER_TEMP="${root}/runner" \
    GITHUB_OUTPUT="${root}/runner/github-output" \
    DOCKER_LOG="${root}/docker.log" \
    DOCKER_OBSERVATIONS="${root}/docker-observations" \
    BACKEND_TAG="ghcr.io/lehigh-university-libraries/scribe:pr-75" \
    FRONTEND_TAG="ghcr.io/lehigh-university-libraries/scribe-frontend:pr-75" \
    SKOPEO_IMAGE="quay.io/skopeo/stable:v1.22.2@sha256:8d25aabcf965e267b6a6ad02ff8da5512f77de1490063625093ff564797e88bc" \
    "$@" \
    "$publisher"
}

setup_case success
success_root="${tmp}/success"
run_publisher "$success_root"

expected_output="$(cat <<'EOF'
backend_image=ghcr.io/lehigh-university-libraries/scribe@sha256:0000000000000000000000000000000000000000000000000000000000000000
frontend_image=ghcr.io/lehigh-university-libraries/scribe-frontend@sha256:0000000000000000000000000000000000000000000000000000000000000001
EOF
)"
[[ "$(<"${success_root}/runner/github-output")" == "$expected_output" ]] ||
  fail "publisher did not emit the expected digest-pinned outputs"
[[ "$(wc -l <"${success_root}/docker.log")" -eq 4 ]] ||
  fail "publisher must make exactly two copy and two inspect calls"

for required_argument in \
  '--read-only' \
  '--cap-drop=ALL' \
  'no-new-privileges' \
  'TMPDIR=/var/tmp' \
  '/tmp:rw,nosuid,nodev,noexec,size=64m' \
  "${success_root}/runner/preview-images:/images:ro" \
  "${success_root}/home/.docker/config.json:/auth/config.json:ro"; do
  grep -Fq -- "$required_argument" "${success_root}/docker.log" ||
    fail "Skopeo container is missing required argument: $required_argument"
done
grep -Fq -- $'--user\t'"$(id -u):$(id -g)" "${success_root}/docker.log" ||
  fail "Skopeo container must run as the host runner UID and GID"
if grep -Fq -- $'--tmpfs\t/var/tmp:' "${success_root}/docker.log"; then
  fail "/var/tmp must not use a fixed-size tmpfs"
fi

work_dir="$(cut -f1 "${success_root}/docker-observations" | sort -u)"
[[ -n "$work_dir" && "$(wc -l <<<"$work_dir")" -eq 1 ]] ||
  fail "all Skopeo calls must share one private temporary directory"
[[ "$(cut -f2 "${success_root}/docker-observations" | sort -u)" == "700" ]] ||
  fail "private Skopeo temporary directory must use mode 0700"
[[ "$work_dir" == "${success_root}/runner/"scribe-preview-publish.* ]] ||
  fail "Skopeo temporary directory must be created under RUNNER_TEMP"
[[ ! -e "$work_dir" ]] || fail "private Skopeo temporary directory was not cleaned"

setup_case copy-failure
copy_failure_root="${tmp}/copy-failure"
if run_publisher "$copy_failure_root" \
  DOCKER_FAIL_COPY=true >"${copy_failure_root}/stdout" 2>"${copy_failure_root}/stderr"; then
  fail "publisher ignored a Skopeo copy failure"
fi
failed_work_dir="$(cut -f1 "${copy_failure_root}/docker-observations" | sort -u)"
[[ -n "$failed_work_dir" && ! -e "$failed_work_dir" ]] ||
  fail "private Skopeo temporary directory was not cleaned after failure"
[[ ! -s "${copy_failure_root}/runner/github-output" ]] ||
  fail "a failed copy must not publish image outputs"

setup_case invalid-ref
invalid_ref_root="${tmp}/invalid-ref"
if run_publisher "$invalid_ref_root" \
  BACKEND_TAG="ghcr.io/example/attacker:pr-75" >"${invalid_ref_root}/stdout" 2>"${invalid_ref_root}/stderr"; then
  fail "publisher accepted an unexpected backend registry path"
fi
[[ ! -s "${invalid_ref_root}/docker.log" ]] ||
  fail "invalid image references must fail before Docker runs"

setup_case floating-skopeo
floating_root="${tmp}/floating-skopeo"
if run_publisher "$floating_root" \
  SKOPEO_IMAGE="quay.io/skopeo/stable:v1.22.2" >"${floating_root}/stdout" 2>"${floating_root}/stderr"; then
  fail "publisher accepted an unpinned Skopeo image"
fi
[[ ! -s "${floating_root}/docker.log" ]] ||
  fail "an unpinned Skopeo image must fail before Docker runs"

setup_case symlink-archive
symlink_root="${tmp}/symlink-archive"
mv "${symlink_root}/runner/preview-images/scribe-backend.oci.tar" \
  "${symlink_root}/runner/backend-outside.oci.tar"
ln -s "${symlink_root}/runner/backend-outside.oci.tar" \
  "${symlink_root}/runner/preview-images/scribe-backend.oci.tar"
if run_publisher "$symlink_root" >"${symlink_root}/stdout" 2>"${symlink_root}/stderr"; then
  fail "publisher accepted a symlinked OCI archive"
fi
[[ ! -s "${symlink_root}/docker.log" ]] ||
  fail "an invalid archive path must fail before Docker runs"

echo "Publish preview images contracts passed."
