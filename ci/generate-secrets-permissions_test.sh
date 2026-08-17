#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-generate-secrets-test.XXXXXX")"
trap 'rm -rf -- "$TEST_DIR"' EXIT

fail() {
  echo "Generate-secrets contract failed: $*" >&2
  exit 1
}

project="$TEST_DIR/project"
mkdir -p "$TEST_DIR/bin" "$project/secrets" "$TEST_DIR/tmp"
cp "$ROOT_DIR/generate-secrets.sh" "$project/generate-secrets.sh"
touch "$project/docker-compose.yaml" "$project/.env"

credential="$project/secrets/GOOGLE_APPLICATION_CREDENTIALS"
database_password="$project/secrets/mariadb_password"
root_password="$project/secrets/mariadb_root_password"
page_key="$project/secrets/page_token_signing_key"
presentation_token="$project/secrets/triplet_presentation_write_token"
source_token="$project/secrets/triplet_source_read_token"

printf '%s' '{}' >"$credential"
printf '%s' 'database-password' >"$database_password"
printf '%s' '0123456789abcdef0123456789abcdef' >"$root_password"
printf '%s' '0123456789abcdef0123456789abcdef' >"$presentation_token"
chmod 0440 "$credential"
chmod 0600 "$database_password" "$root_password" "$presentation_token"
credential_stat_before="$(stat -c '%u:%g:%a' "$credential")"
credential_sha_before="$(sha256sum "$credential" | awk '{print $1}')"
local_hashes_before="$(
  sha256sum "$database_password" "$root_password" "$presentation_token"
)"

cat >"$TEST_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "$*" == *"config --format json"* ]]; then
  jq -n \
    --arg credential "$TEST_CREDENTIAL" \
    --arg database "$TEST_DATABASE_PASSWORD" \
    --arg root "$TEST_ROOT_PASSWORD" \
    --arg page "$TEST_PAGE_KEY" \
    --arg presentation "$TEST_PRESENTATION_TOKEN" \
    --arg source "$TEST_SOURCE_TOKEN" \
    --arg vault_address "${TEST_VAULT_ADDRESS:-}" \
    '{
      secrets: {
        GOOGLE_APPLICATION_CREDENTIALS: {file: $credential},
        mariadb_password: {file: $database},
        mariadb_root_password: {file: $root},
        page_token_signing_key: {file: $page},
        triplet_presentation_write_token: {file: $presentation},
        triplet_source_read_token: {file: $source}
      },
      services: {
        "vault-init": {
          environment: {
            VAULT_ADDRESS: $vault_address
          }
        }
      }
    }'
  exit 0
fi
if [[ "$*" == *"ps --all --quiet api worker triplet"* ]]; then
  printf '%s' "${TEST_RUNNING_CONSUMER_IDS:-}"
  exit 0
fi
if [[ "$*" == *"--profile init run --rm -T vault-init"* ]]; then
  exit "${TEST_VAULT_RUN_STATUS:-0}"
fi
echo "unexpected docker invocation: $*" >&2
exit 1
EOF
chmod 0755 "$TEST_DIR/bin/docker"

cat >"$TEST_DIR/bin/chmod" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
for argument in "$@"; do
  if [[ "$argument" == "$TEST_CREDENTIAL" ]]; then
    echo "generate-secrets attempted to chmod the externally managed credential" >&2
    exit 97
  fi
done
exec /bin/chmod "$@"
EOF
chmod 0755 "$TEST_DIR/bin/chmod"

cat >"$TEST_DIR/bin/chgrp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
for argument in "$@"; do
  if [[ "$argument" == "$TEST_CREDENTIAL" || "$argument" == "-R" ]]; then
    echo "generate-secrets attempted to chgrp the externally managed credential" >&2
    exit 98
  fi
done
exec /bin/chgrp "$@"
EOF
chmod 0755 "$TEST_DIR/bin/chgrp"

run_generator() {
  PATH="$TEST_DIR/bin:$PATH" \
    TMPDIR="$TEST_DIR/tmp" \
    TEST_CREDENTIAL="$credential" \
    TEST_DATABASE_PASSWORD="$database_password" \
    TEST_ROOT_PASSWORD="$root_password" \
    TEST_PAGE_KEY="$page_key" \
    TEST_PRESENTATION_TOKEN="$presentation_token" \
    TEST_SOURCE_TOKEN="$source_token" \
    TEST_VAULT_ADDRESS="${TEST_VAULT_ADDRESS:-}" \
    TEST_VAULT_RUN_STATUS="${TEST_VAULT_RUN_STATUS:-0}" \
    TEST_RUNNING_CONSUMER_IDS="${TEST_RUNNING_CONSUMER_IDS:-}" \
    TEST_UNAME_KERNEL="${TEST_UNAME_KERNEL:-}" \
    SCRIBE_REPAIR_LOCAL_TOKENS="${TEST_REPAIR_LOCAL_TOKENS:-true}" \
    bash "$project/generate-secrets.sh"
}

non_linux_bin="$TEST_DIR/non-linux-bin"
mkdir -p "$non_linux_bin"
cat >"$non_linux_bin/uname" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "${TEST_UNAME_KERNEL:?}"
EOF
chmod 0755 "$non_linux_bin/uname"

success_output="$TEST_DIR/success.out"
run_generator >"$success_output" 2>&1 ||
  {
    sed -n '1,120p' "$success_output" >&2
    fail "valid local secret state was rejected"
  }
expected_success_trace=$'SCRIBE_APP_INIT_TRACE_V1 stage=secrets-script-start\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-directory-ready\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-group-ready\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-env-ready\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-compose-list-start\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-compose-list-ready\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-files-ready\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-vault-config-start\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-vault-config-ready\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-vault-sync-complete\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-permissions-start\nSCRIBE_APP_INIT_TRACE_V1 stage=secrets-permissions-complete'
[[ "$(grep '^SCRIBE_APP_INIT_TRACE_V1 ' "$success_output")" == "$expected_success_trace" ]] ||
  fail "successful generation emitted an unexpected fixed trace sequence"

[[ "$(stat -c '%u:%g:%a' "$credential")" == "$credential_stat_before" ]] ||
  fail "the externally managed credential metadata changed"
[[ "$(sha256sum "$credential" | awk '{print $1}')" == "$credential_sha_before" ]] ||
  fail "the externally managed credential contents changed"
[[ "$(
  sha256sum "$database_password" "$root_password" "$presentation_token"
)" == "$local_hashes_before" ]] ||
  fail "an existing locally managed secret was overwritten"
for secret in \
  "$database_password" "$root_password" "$page_key" \
  "$presentation_token" "$source_token"; do
  [[ "$(stat -c '%a' "$secret")" == 640 ]] ||
    fail "a locally managed secret was not normalized to mode 0640"
done
[[ "$(wc -c <"$database_password")" -gt 0 ]] ||
  fail "the Vault-sourced database password is empty"
for secret in "$root_password" "$page_key" "$presentation_token" "$source_token"; do
  [[ "$(wc -c <"$secret")" -ge 32 ]] ||
    fail "a generated secret is too short"
done
[[ "$(stat -c '%a' "$project/secrets")" == 750 ]] ||
  fail "the secret directory was not normalized"
grep -Eq '^SCRIBE_SECRETS_GID=[0-9]+$' "$project/.env" ||
  fail "the Compose secret group was not persisted"
initial_generation="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
[[ "$initial_generation" =~ ^[A-Za-z0-9]{32}$ ]] ||
  fail "the full lifecycle did not record creation of a missing application token"

hashes_before="$(
  sha256sum \
    "$credential" "$database_password" "$root_password" "$page_key" \
    "$presentation_token" "$source_token"
)"
run_generator >"$TEST_DIR/repeated.out" 2>&1 ||
  fail "a repeated generation run was not idempotent"
hashes_after="$(
  sha256sum \
    "$credential" "$database_password" "$root_password" "$page_key" \
    "$presentation_token" "$source_token"
)"
[[ "$hashes_before" == "$hashes_after" ]] ||
  fail "a repeated generation run rotated existing secret bytes"
[[ "$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")" == \
  "$initial_generation" ]] ||
  fail "a repeated generation run changed the local-token Compose generation"

# A missing app-owned token after bootstrap is a live credential rotation, not
# ordinary file scaffolding. A partial lifecycle must refuse while any prior
# consumer container exists; the full lifecycle creates it and advances the
# generation that forces those consumers to be recreated.
rm "$source_token"
generation_before_missing="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
if TEST_REPAIR_LOCAL_TOKENS=false TEST_RUNNING_CONSUMER_IDS=existing-api \
  run_generator >"$TEST_DIR/missing-repair-disabled.out" 2>&1; then
  fail "a partial lifecycle recreated a missing live application token"
fi
[[ ! -e "$source_token" ]] ||
  fail "disabled missing-token recovery created a replacement"
[[ "$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")" == \
  "$generation_before_missing" ]] ||
  fail "disabled missing-token recovery changed the Compose generation"
run_generator >"$TEST_DIR/missing-repair-full.out" 2>&1 ||
  fail "the full lifecycle did not recreate a missing application token"
generation_after_missing="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
[[ "$(wc -c <"$source_token")" -eq 32 &&
  "$generation_after_missing" =~ ^[A-Za-z0-9]{32}$ &&
  "$generation_after_missing" != "$generation_before_missing" ]] ||
  fail "full missing-token recovery did not create and publish one generation"
rm "$source_token"
if ! PATH="$non_linux_bin:$PATH" TEST_UNAME_KERNEL=Darwin \
  TEST_REPAIR_LOCAL_TOKENS=false TEST_RUNNING_CONSUMER_IDS='' \
  run_generator >"$TEST_DIR/missing-first-bootstrap.out" 2>&1; then
  fail "a partial database lifecycle could not scaffold before consumers exist"
fi
[[ "$(wc -c <"$source_token")" -eq 32 &&
  "$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")" == \
    "$generation_after_missing" ]] ||
  fail "consumer-free scaffolding unexpectedly changed the Compose generation"

# Short application-owned tokens are invalid and safe to regenerate. Each
# repair preserves its bind-mounted inode and changes the nonsecret Compose
# generation consumed by every file-to-environment token consumer.
printf '%s' 'short' >"$source_token"
short_source_inode="$(stat -c '%d:%i' "$source_token")"
generation_before_disabled="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
if TEST_REPAIR_LOCAL_TOKENS=false \
  run_generator >"$TEST_DIR/short-repair-disabled.out" 2>&1; then
  fail "a partial lifecycle repaired a live application token"
fi
[[ "$(<"$source_token")" == "short" &&
  "$(stat -c '%d:%i' "$source_token")" == "$short_source_inode" ]] ||
  fail "disabled application-token repair mutated the token"
[[ "$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")" == \
  "$generation_before_disabled" ]] ||
  fail "disabled application-token repair changed the Compose generation"

# Darwin and BSD descriptor files can share the locked descriptor's file
# offset and ignore truncation flags. Refuse their in-place path before either
# the token or its consumer-recreation generation changes.
for unsupported_kernel in Darwin FreeBSD; do
  unsupported_metadata="$(stat -c '%d:%i:%a' "$source_token")"
  unsupported_generation="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
  if PATH="$non_linux_bin:$PATH" TEST_UNAME_KERNEL="$unsupported_kernel" \
    run_generator >"$TEST_DIR/short-repair-${unsupported_kernel}.out" 2>&1; then
    fail "${unsupported_kernel} unexpectedly repaired a short application token"
  fi
  [[ "$(<"$source_token")" == "short" &&
    "$(stat -c '%d:%i:%a' "$source_token")" == "$unsupported_metadata" ]] ||
    fail "${unsupported_kernel} short-token refusal mutated the token"
  [[ "$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")" == \
    "$unsupported_generation" ]] ||
    fail "${unsupported_kernel} short-token refusal changed the Compose generation"
  grep -Fq 'supported only on Linux with /proc' \
    "$TEST_DIR/short-repair-${unsupported_kernel}.out" ||
    fail "${unsupported_kernel} refusal did not explain the platform boundary"
  grep -Fq "Run 'make down', remove only the reported short token file, then run 'make up'" \
    "$TEST_DIR/short-repair-${unsupported_kernel}.out" ||
    fail "${unsupported_kernel} refusal did not provide safe regeneration guidance"
done

repair_index=0
for repaired_token in "$page_key" "$presentation_token" "$source_token"; do
  repair_index=$((repair_index + 1))
  if [[ "$repair_index" -eq 1 ]]; then
    # Consumers strip CR/LF before validating; 32 raw bytes can still be a
    # short 31-byte effective token.
    printf '1234567890123456789012345678901\n' >"$repaired_token"
  else
    printf '%s' 'short' >"$repaired_token"
  fi
  repaired_inode="$(stat -c '%d:%i' "$repaired_token")"
  generation_before="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
  run_generator >"$TEST_DIR/short-repair-${repair_index}.out" 2>&1 ||
    fail "a short locally generated token was not repaired"
  [[ "$(wc -c <"$repaired_token")" -eq 32 ]] ||
    fail "the repaired locally generated token has the wrong length"
  [[ "$(stat -c '%d:%i' "$repaired_token")" == "$repaired_inode" ]] ||
    fail "short-token recovery replaced the bind-mounted inode"
  grep -Fq 'Repairing short locally generated secret:' \
    "$TEST_DIR/short-repair-${repair_index}.out" ||
    fail "short-token recovery did not emit its bounded diagnostic"
  generation_after="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
  [[ "$generation_after" =~ ^[A-Za-z0-9]{32}$ &&
    "$generation_after" != "$generation_before" ]] ||
    fail "short-token recovery did not change the Compose generation"
done

repaired_source_hash="$(sha256sum "$source_token" | awk '{print $1}')"
repaired_generation="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
run_generator >"$TEST_DIR/short-repair-repeat.out" 2>&1 ||
  fail "a repeated run after short-token repair failed"
[[ "$(sha256sum "$source_token" | awk '{print $1}')" == "$repaired_source_hash" ]] ||
  fail "a repeated run rotated the repaired token"
[[ "$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")" == \
  "$repaired_generation" ]] ||
  fail "a repeated run changed the Compose generation without repairing a token"

# Revalidate effective length after acquiring the descriptor lock. The shim
# models a first repair that publishes a valid same-raw-size token while this
# process waits: 31 characters plus LF and 32 characters both occupy 32 bytes.
flock_swap_bin="$TEST_DIR/flock-swap-bin"
mkdir -p "$flock_swap_bin"
cat >"$flock_swap_bin/flock" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

descriptor="${!#}"
printf '%s' 'known-valid-token-12345678901234' >"/proc/self/fd/${descriptor}"
EOF
chmod 0755 "$flock_swap_bin/flock"

run_generator_with_concurrent_valid_token() {
  PATH="$flock_swap_bin:$TEST_DIR/bin:$PATH" \
    TMPDIR="$TEST_DIR/tmp" \
    TEST_CREDENTIAL="$credential" \
    TEST_DATABASE_PASSWORD="$database_password" \
    TEST_ROOT_PASSWORD="$root_password" \
    TEST_PAGE_KEY="$page_key" \
    TEST_PRESENTATION_TOKEN="$presentation_token" \
    TEST_SOURCE_TOKEN="$source_token" \
    SCRIBE_REPAIR_LOCAL_TOKENS=true \
    bash "$project/generate-secrets.sh"
}

printf '1234567890123456789012345678901\n' >"$source_token"
concurrent_inode="$(stat -c '%d:%i' "$source_token")"
generation_before_concurrent="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
run_generator_with_concurrent_valid_token \
  >"$TEST_DIR/concurrent-valid-token.out" 2>&1 ||
  fail "a concurrently completed token repair made convergence fail"
[[ "$(<"$source_token")" == "known-valid-token-12345678901234" &&
  "$(stat -c '%d:%i' "$source_token")" == "$concurrent_inode" ]] ||
  fail "a second repair rotated the token published while waiting for its lock"
grep -Fq 'Application token became valid while waiting for its repair lock; preserving it.' \
  "$TEST_DIR/concurrent-valid-token.out" ||
  fail "the post-lock effective-length check did not run"
generation_after_concurrent="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
[[ "$generation_after_concurrent" =~ ^[A-Za-z0-9]{32}$ &&
  "$generation_after_concurrent" != "$generation_before_concurrent" ]] ||
  fail "the concurrent repair did not retain a consumer-recreation generation"

# A cooperating concurrent repair cannot mutate the descriptor while its lock
# is held. Generation advances before the attempt, so a later full retry still
# recreates consumers after it acquires the lock and repairs the same inode.
if command -v flock >/dev/null 2>&1; then
  printf '%s' 'locked-short' >"$source_token"
  locked_inode="$(stat -c '%d:%i' "$source_token")"
  generation_before_lock="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
  exec {held_secret_fd}<>"$source_token"
  flock -n "$held_secret_fd" || fail "could not hold the secret repair test lock"
  if run_generator >"$TEST_DIR/locked-repair.out" 2>&1; then
    fail "a locked application token was repaired concurrently"
  fi
  [[ "$(<"$source_token")" == "locked-short" &&
    "$(stat -c '%d:%i' "$source_token")" == "$locked_inode" ]] ||
    fail "a rejected concurrent repair mutated the token"
  generation_after_lock="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
  [[ "$generation_after_lock" =~ ^[A-Za-z0-9]{32}$ &&
    "$generation_after_lock" != "$generation_before_lock" ]] ||
    fail "a rejected concurrent repair did not preserve future recreation"
  flock -u "$held_secret_fd"
  exec {held_secret_fd}>&-
  run_generator >"$TEST_DIR/locked-repair-retry.out" 2>&1 ||
    fail "a locked application token was not repairable after release"
  [[ "$(wc -c <"$source_token")" -eq 32 &&
    "$(stat -c '%d:%i' "$source_token")" == "$locked_inode" ]] ||
    fail "the post-lock retry did not repair the same inode"
fi

# Simulate a failed regular-file write after a few bytes. Recovery must leave
# the verified inode empty (and therefore retryable), while the generation
# change recorded before publication still forces consumer recreation.
run_generator_with_partial_repair_write() {
  PATH="$TEST_DIR/bin:$PATH" \
    TMPDIR="$TEST_DIR/tmp" \
    TEST_CREDENTIAL="$credential" \
    TEST_DATABASE_PASSWORD="$database_password" \
    TEST_ROOT_PASSWORD="$root_password" \
    TEST_PAGE_KEY="$page_key" \
    TEST_PRESENTATION_TOKEN="$presentation_token" \
    TEST_SOURCE_TOKEN="$source_token" \
    TEST_PARTIAL_WRITE_MARKER="$TEST_DIR/partial-write-triggered" \
    SCRIBE_REPAIR_LOCAL_TOKENS=true \
    bash --noprofile --norc -c '
      printf() {
        local format="${1-}" value="${2-}"
        if [[ "$format" == "%s" && "${#value}" -eq 32 &&
          ! -e "$TEST_PARTIAL_WRITE_MARKER" ]]; then
          builtin printf "%s" "${value:0:7}"
          : >"$TEST_PARTIAL_WRITE_MARKER"
          return 1
        fi
        builtin printf "$@"
      }
      export -f printf
      exec bash "$1"
    ' _ "$project/generate-secrets.sh"
}

printf '%s' 'partial' >"$source_token"
partial_inode="$(stat -c '%d:%i' "$source_token")"
generation_before_partial="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
if run_generator_with_partial_repair_write >"$TEST_DIR/partial-write.out" 2>&1; then
  fail "a partial replacement-secret write was accepted"
fi
[[ ! -s "$source_token" ]] ||
  fail "a partial replacement-secret write left a nonretryable short token"
[[ "$(stat -c '%d:%i' "$source_token")" == "$partial_inode" ]] ||
  fail "partial-write recovery replaced the bind-mounted inode"
generation_after_partial="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
[[ "$generation_after_partial" =~ ^[A-Za-z0-9]{32}$ &&
  "$generation_after_partial" != "$generation_before_partial" ]] ||
  fail "partial-write recovery did not force future consumer recreation"
run_generator >"$TEST_DIR/partial-write-retry.out" 2>&1 ||
  fail "a zeroed partial-write failure was not retryable"
[[ "$(wc -c <"$source_token")" -eq 32 &&
  "$(stat -c '%d:%i' "$source_token")" == "$partial_inode" ]] ||
  fail "partial-write retry did not repair the same inode"

# A metadata-verification failure after publication follows the same rollback
# path. The stat shim permits the pre-write descriptor check, then rejects both
# post-write GNU/BSD variants.
real_stat="$(command -v stat)"
stat_failure_bin="$TEST_DIR/stat-failure-bin"
mkdir -p "$stat_failure_bin"
cat >"$stat_failure_bin/stat" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

for argument in "$@"; do
  if [[ "$argument" == /proc/self/fd/* ]]; then
    count=0
    if [[ -f "$TEST_STAT_COUNT" ]]; then
      count="$(<"$TEST_STAT_COUNT")"
    fi
    count=$((count + 1))
    printf '%s\n' "$count" >"$TEST_STAT_COUNT"
    if ((count > 1)); then
      exit 91
    fi
    break
  fi
done
exec "$TEST_REAL_STAT" "$@"
EOF
chmod 0755 "$stat_failure_bin/stat"

run_generator_with_post_write_stat_failure() {
  PATH="$stat_failure_bin:$TEST_DIR/bin:$PATH" \
    TMPDIR="$TEST_DIR/tmp" \
    TEST_CREDENTIAL="$credential" \
    TEST_DATABASE_PASSWORD="$database_password" \
    TEST_ROOT_PASSWORD="$root_password" \
    TEST_PAGE_KEY="$page_key" \
    TEST_PRESENTATION_TOKEN="$presentation_token" \
    TEST_SOURCE_TOKEN="$source_token" \
    TEST_STAT_COUNT="$TEST_DIR/stat-count" \
    TEST_REAL_STAT="$real_stat" \
    SCRIBE_REPAIR_LOCAL_TOKENS=true \
    bash "$project/generate-secrets.sh"
}

printf '%s' 'short-again' >"$source_token"
stat_failure_inode="$(stat -c '%d:%i' "$source_token")"
generation_before_stat_failure="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
rm -f "$TEST_DIR/stat-count"
if run_generator_with_post_write_stat_failure \
  >"$TEST_DIR/post-write-stat-failure.out" 2>&1; then
  fail "post-write metadata verification failure was accepted"
fi
[[ ! -s "$source_token" &&
  "$(stat -c '%d:%i' "$source_token")" == "$stat_failure_inode" ]] ||
  fail "post-write metadata failure did not leave the same inode retryable"
generation_after_stat_failure="$(sed -n 's/^SCRIBE_LOCAL_TOKEN_GENERATION=//p' "$project/.env")"
[[ "$generation_after_stat_failure" =~ ^[A-Za-z0-9]{32}$ &&
  "$generation_after_stat_failure" != "$generation_before_stat_failure" ]] ||
  fail "post-write metadata failure did not force future consumer recreation"
run_generator >"$TEST_DIR/post-write-stat-retry.out" 2>&1 ||
  fail "post-write metadata failure was not retryable"
[[ "$(wc -c <"$source_token")" -eq 32 &&
  "$(stat -c '%d:%i' "$source_token")" == "$stat_failure_inode" ]] ||
  fail "post-write metadata retry did not repair the same inode"

set +e
TEST_VAULT_ADDRESS='http://vault.test:8200' TEST_VAULT_RUN_STATUS=23 \
  run_generator >"$TEST_DIR/vault-failure.out" 2>&1
vault_status=$?
set -e
[[ "$vault_status" -eq 23 ]] ||
  fail "the Vault sync failure status was not preserved"
[[ "$(grep '^SCRIBE_APP_INIT_TRACE_V1 ' "$TEST_DIR/vault-failure.out" | tail -n 1)" == \
  'SCRIBE_APP_INIT_TRACE_V1 stage=secrets-vault-run-failed' ]] ||
  fail "Vault failure did not stop at its fixed failure stage"
if grep -Fq 'SCRIBE_APP_INIT_TRACE_V1 stage=secrets-permissions-start' \
  "$TEST_DIR/vault-failure.out"; then
  fail "permission normalization ran after a failed Vault sync"
fi

rm -f "$source_token"
outside="$TEST_DIR/outside-secret"
printf '%s' '0123456789abcdef0123456789abcdef' >"$outside"
outside_sha="$(sha256sum "$outside" | awk '{print $1}')"
ln -s "$outside" "$source_token"
if run_generator >"$TEST_DIR/symlink.out" 2>&1; then
  fail "a symlinked secret was accepted"
fi
[[ "$(sha256sum "$outside" | awk '{print $1}')" == "$outside_sha" ]] ||
  fail "a rejected secret symlink changed its target"

rm -f "$source_token"
ln "$outside" "$source_token"
if run_generator >"$TEST_DIR/hardlink.out" 2>&1; then
  fail "a hard-linked secret was accepted"
fi
[[ "$(sha256sum "$outside" | awk '{print $1}')" == "$outside_sha" ]] ||
  fail "a rejected secret hard link changed its target"

rm -f "$source_token" "$outside"
mkdir "$source_token"
if run_generator >"$TEST_DIR/nonregular.out" 2>&1; then
  fail "a non-regular secret path was accepted"
fi
grep -Fq 'linked or non-regular' "$TEST_DIR/nonregular.out" ||
  fail "a non-regular secret did not produce a bounded diagnostic"

echo "Compose-derived secrets converge without overwriting existing bytes or managed credentials."
