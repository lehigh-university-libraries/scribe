#!/bin/sh

# Bounded retry helper for Vault bootstrap. Callers own error details; this
# helper deliberately logs only an operation label and attempt count so Vault
# response bodies and credentials can never reach deployment logs.
vault_retry() {
  _vault_retry_label="$1"
  shift
  _vault_retry_limit="${VAULT_RETRY_ATTEMPTS:-18}"
  _vault_retry_delay="${VAULT_RETRY_INITIAL_DELAY_SECONDS:-1}"
  _vault_retry_maximum_delay="${VAULT_RETRY_MAX_DELAY_SECONDS:-10}"

  case "$_vault_retry_limit" in ''|*[!0-9]*|0) echo "Invalid Vault retry configuration." >&2; return 2 ;; esac
  case "$_vault_retry_delay" in ''|*[!0-9]*) echo "Invalid Vault retry configuration." >&2; return 2 ;; esac
  case "$_vault_retry_maximum_delay" in ''|*[!0-9]*) echo "Invalid Vault retry configuration." >&2; return 2 ;; esac
  if [ "$_vault_retry_limit" -gt 30 ] || [ "$_vault_retry_delay" -gt 30 ] || [ "$_vault_retry_maximum_delay" -gt 30 ]; then
    echo "Invalid Vault retry configuration." >&2
    return 2
  fi

  _vault_retry_attempt=1
  while [ "$_vault_retry_attempt" -le "$_vault_retry_limit" ]; do
    if "$@"; then
      return 0
    fi
    if [ "$_vault_retry_attempt" -eq "$_vault_retry_limit" ]; then
      break
    fi
    echo "${_vault_retry_label} attempt ${_vault_retry_attempt}/${_vault_retry_limit} failed; retrying." >&2
    sleep "$_vault_retry_delay"
    if [ "$_vault_retry_delay" -lt "$_vault_retry_maximum_delay" ]; then
      _vault_retry_delay=$((_vault_retry_delay * 2))
      if [ "$_vault_retry_delay" -gt "$_vault_retry_maximum_delay" ]; then
        _vault_retry_delay="$_vault_retry_maximum_delay"
      fi
    fi
    _vault_retry_attempt=$((_vault_retry_attempt + 1))
  done
  echo "${_vault_retry_label} failed after ${_vault_retry_limit} attempts." >&2
  return 1
}
