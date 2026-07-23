#!/usr/bin/env bash

set -euo pipefail

mode="${DEPLOY_MODE:?DEPLOY_MODE is required}"
pr_number="${PR_NUMBER:-}"

outcome() {
  local name="$1"
  local value="${!name:-skipped}"
  case "$value" in success|failure|cancelled|skipped) printf '%s' "$value" ;;
    *) echo "invalid step outcome for $name: $value" >&2; exit 2 ;;
  esac
}

case "$mode" in
  apply)
    if [ -n "$pr_number" ]; then
      plan="$(outcome PLAN_PREVIEW_OUTCOME)"
      primary="$(outcome APPLY_PREVIEW_OUTCOME)"
    else
      plan="$(outcome PLAN_OUTCOME)"
      primary="$(outcome APPLY_OUTCOME)"
    fi
    if [ "$plan" != "success" ]; then
      printf '%s\n' "$plan"
      exit 0
    fi
    if [ "$primary" != "success" ]; then
      if [ -z "$pr_number" ] && [ "$(outcome ROLLBACK_OUTCOME)" = "failure" ]; then
        printf 'rollback-failed\n'
      elif [ -z "$pr_number" ] && [ "$(outcome ROLLBACK_OUTCOME)" = "success" ]; then
        printf 'apply-failed-rolled-back\n'
      else
        printf '%s\n' "$primary"
      fi
      exit 0
    fi
    revision="$(outcome REVISION_OUTCOME)"
    if [ "$revision" != "success" ]; then
      if [ -z "$pr_number" ] && [ "$(outcome ROLLBACK_OUTCOME)" = "failure" ]; then
        printf 'rollback-failed\n'
      elif [ -z "$pr_number" ] && [ "$(outcome ROLLBACK_OUTCOME)" = "success" ]; then
        printf 'attestation-failed-rolled-back\n'
      else
        printf 'attestation-%s\n' "$revision"
      fi
      exit 0
    fi
    readiness="$(outcome READINESS_OUTCOME)"
    if [ "$readiness" != "success" ]; then
      if [ -z "$pr_number" ] && [ "$(outcome ROLLBACK_OUTCOME)" = "failure" ]; then
        printf 'rollback-failed\n'
      elif [ -z "$pr_number" ] && [ "$(outcome ROLLBACK_OUTCOME)" = "success" ]; then
        printf 'readiness-failed-rolled-back\n'
      else
        printf '%s\n' "$readiness"
      fi
      exit 0
    fi
    url="$(outcome URL_OUTCOME)"
    if [ "$url" != "success" ]; then
      printf 'url-%s\n' "$url"
    elif [ -z "$pr_number" ] && [ "$(outcome BACKUP_OUTCOME)" != "success" ]; then
      printf 'backup-verification-%s\n' "$(outcome BACKUP_OUTCOME)"
    else
      printf 'success\n'
    fi
    ;;
  plan)
    if [ -n "$pr_number" ]; then outcome PLAN_PREVIEW_OUTCOME; else outcome PLAN_OUTCOME; fi
    printf '\n'
    ;;
  destroy)
    if [ -n "$pr_number" ]; then outcome DESTROY_PREVIEW_OUTCOME; else outcome DESTROY_OUTCOME; fi
    printf '\n'
    ;;
  *) echo "DEPLOY_MODE must be apply, plan, or destroy" >&2; exit 2 ;;
esac
