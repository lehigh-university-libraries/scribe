#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
source_paths_script="${repo_root}/ci/ocr-source-paths.sh"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/scribe-ocr-source-paths-test.XXXXXX")"
trap 'rm -rf -- "${test_root}"' EXIT

fail() {
  echo "OCR source path contract failed: $*" >&2
  exit 1
}

first_output="${test_root}/first"
second_output="${test_root}/second"
sorted_output="${test_root}/sorted"

(cd "${test_root}" && "${source_paths_script}") >"${first_output}"
(cd / && "${source_paths_script}") >"${second_output}"
cmp -s "${first_output}" "${second_output}" ||
  fail "output depends on the caller's working directory"

LC_ALL=C sort -u "${first_output}" >"${sorted_output}"
cmp -s "${first_output}" "${sorted_output}" ||
  fail "output is not deterministically sorted and unique"

fixed_paths=(
  .dockerignore
  .github/workflows/build-ocr.yaml
  Dockerfile.segmentor
  Makefile
  ci/generate-ocr-images-map.sh
  ci/ocr-matrix.sh
  ci/ocr-source-paths.sh
  ci/segmentor-lock.sh
  config/imagemagick-policy.xml
  config/ocr.yaml
  config/segmentor-requirements.lock
  go.mod
  go.sum
  scripts/install-kraken-models.sh
  terraform/modules/ollama-cloud-run/image
)
for path in "${fixed_paths[@]}"; do
  grep -Fxq "${path}" "${first_output}" ||
    fail "required source path is missing: ${path}"
done

for required_package in \
  cmd/segmentor \
  internal/config \
  internal/imageservice \
  internal/segmentor \
  internal/worddetection; do
  grep -Fxq "${required_package}" "${first_output}" ||
    fail "required segmentor dependency is missing: ${required_package}"
done

if grep -Fxq internal/server "${first_output}"; then
  fail "an unrelated Go package was included"
fi

# Independently project every in-module dependency import path back to its
# repository directory. This keeps the test current when ./cmd/segmentor gains
# another internal dependency rather than relying only on a hand-maintained
# sample of today's closure.
module_path="$(
  cd "${repo_root}"
  CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go list -tags localocr -m -f '{{.Path}}'
)"
if ! package_imports="$(
  cd "${repo_root}"
  CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go list -tags localocr -deps \
      -f '{{if and .Module .Module.Main}}{{.ImportPath}}{{end}}' \
      ./cmd/segmentor
)"; then
  fail "could not independently resolve the segmentor dependency closure"
fi

expected_paths="${test_root}/expected"
{
  printf '%s\n' "${fixed_paths[@]}"
  while IFS= read -r import_path; do
    [ -n "${import_path}" ] || continue
    case "${import_path}" in
      "${module_path}"/*)
        printf '%s\n' "${import_path#"${module_path}/"}"
        ;;
      *)
        fail "go list identified an unexpected in-module import: ${import_path}"
        ;;
    esac
  done <<<"${package_imports}"
} | LC_ALL=C sort -u >"${expected_paths}"

if ! diff -u "${expected_paths}" "${first_output}"; then
  fail "emitted paths differ from the fixed inputs and complete Go closure"
fi

while IFS= read -r path; do
  case "${path}" in
    /*|.|..|../*|*/../*)
      fail "path is not a bounded repository-relative pathspec: ${path}"
      ;;
  esac
  [ -e "${repo_root}/${path}" ] ||
    fail "emitted path does not exist: ${path}"
done <"${first_output}"

# A directory pathspec is intentionally used for the Ollama context. Prove it
# detects both added and deleted files at arbitrary depths without a shell glob.
pathspec_fixture="${test_root}/pathspec-fixture"
ollama_context="terraform/modules/ollama-cloud-run/image"
mkdir -p "${pathspec_fixture}/${ollama_context}/nested"
git -C "${pathspec_fixture}" init -q
git -C "${pathspec_fixture}" config user.name "OCR Source Path Contract"
git -C "${pathspec_fixture}" config user.email "ocr-source-paths@example.invalid"
printf '%s\n' old >"${pathspec_fixture}/${ollama_context}/nested/deleted"
git -C "${pathspec_fixture}" add -- "${ollama_context}"
git -C "${pathspec_fixture}" -c commit.gpgSign=false commit -qm "base"
rm -- "${pathspec_fixture}/${ollama_context}/nested/deleted"
printf '%s\n' new >"${pathspec_fixture}/${ollama_context}/nested/added"
git -C "${pathspec_fixture}" add -A -- "${ollama_context}"
git -C "${pathspec_fixture}" -c commit.gpgSign=false commit -qm "change context"
context_changes="$(
  git -C "${pathspec_fixture}" diff --name-only HEAD^ HEAD -- "${ollama_context}"
)"
grep -Fxq "${ollama_context}/nested/added" <<<"${context_changes}" ||
  fail "Ollama directory pathspec did not detect a nested addition"
grep -Fxq "${ollama_context}/nested/deleted" <<<"${context_changes}" ||
  fail "Ollama directory pathspec did not detect a nested deletion"

echo "OCR source paths cover fixed image inputs and the complete segmentor Go closure."
