#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin"
printf 'reviewed segmentation model\n' > "$test_root/model.mlmodel"
export SEGMENTATION_FIXTURE_SOURCE="$test_root/model.mlmodel"
export KRAKEN_MODEL_DIR="$test_root/models"
export KRAKEN_TMP_DATA_DIR="$test_root/scratch"
export KRAKEN_SEGMENTATION_MODEL_ID=kraken
export KRAKEN_SEGMENTATION_MODEL_FILE=blla.mlmodel
export KRAKEN_SEGMENTATION_MODEL_DOI=10.5281/zenodo.14602569
export KRAKEN_SEGMENTATION_MODEL_SHA256
KRAKEN_SEGMENTATION_MODEL_SHA256="$(sha256sum "$SEGMENTATION_FIXTURE_SOURCE" | awk '{print $1}')"
export KRAKEN_MODEL_DOWNLOAD_ATTEMPTS=2 KRAKEN_MODEL_DOWNLOAD_RETRY_DELAY_SECONDS=0
cat > "$test_root/bin/kraken" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = get ]
[ "$2" = "$KRAKEN_SEGMENTATION_MODEL_DOI" ]
[ "${MOCK_DOWNLOAD_FAIL:-false}" != true ] || exit 1
mkdir -p "$XDG_DATA_HOME/htrmopo"
cp "$SEGMENTATION_FIXTURE_SOURCE" "$XDG_DATA_HOME/htrmopo/$KRAKEN_SEGMENTATION_MODEL_FILE"
FAKE
cat > "$test_root/bin/curl" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
[ "${MOCK_DOWNLOAD_FAIL:-false}" != true ] || exit 1
while (($# > 0)); do
 if [ "$1" = --output ]; then cp "$SEGMENTATION_FIXTURE_SOURCE" "$2"; exit 0; fi
 shift
done
exit 1
FAKE
chmod +x "$test_root/bin/kraken" "$test_root/bin/curl"
export PATH="$test_root/bin:$PATH"
bash "$repo_root/scripts/install-kraken-models.sh"
cmp "$SEGMENTATION_FIXTURE_SOURCE" "$KRAKEN_MODEL_DIR/blla.mlmodel"
# A verified cached artifact needs no download.
MOCK_DOWNLOAD_FAIL=true bash "$repo_root/scripts/install-kraken-models.sh"
printf 'corrupt\n' > "$KRAKEN_MODEL_DIR/blla.mlmodel"
bash "$repo_root/scripts/install-kraken-models.sh"
cmp "$SEGMENTATION_FIXTURE_SOURCE" "$KRAKEN_MODEL_DIR/blla.mlmodel"
rm "$KRAKEN_MODEL_DIR/blla.mlmodel"
if KRAKEN_SEGMENTATION_MODEL_SHA256="$(printf '%064d' 0)" bash "$repo_root/scripts/install-kraken-models.sh" >/dev/null 2>&1; then
 echo 'Installer accepted a tampered artifact' >&2; exit 1
fi
[ ! -e "$KRAKEN_MODEL_DIR/blla.mlmodel" ]
if KRAKEN_SEGMENTATION_MODEL_FILE=../escape.mlmodel bash "$repo_root/scripts/install-kraken-models.sh" >/dev/null 2>&1; then
 echo 'Installer accepted an unsafe path' >&2; exit 1
fi
if MOCK_DOWNLOAD_FAIL=true bash "$repo_root/scripts/install-kraken-models.sh" >/dev/null 2>&1; then
 echo 'Installer accepted an exhausted download' >&2; exit 1
fi
echo 'Kraken segmentation artifact checks passed.'
