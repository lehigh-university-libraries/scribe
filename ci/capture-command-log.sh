#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "Usage: $0 LOG_FILE COMMAND [ARG...]" >&2
  exit 2
fi

log_file="$1"
shift

if [ -z "$log_file" ]; then
  echo "LOG_FILE must not be empty." >&2
  exit 2
fi
if ! command -v awk >/dev/null 2>&1; then
  echo "awk is required to capture a redacted command log." >&2
  exit 127
fi

# GitHub applies ::add-mask:: commands only to the runner log stream. Artifact
# files written by tee receive the unmasked bytes. Keep the workflow command in
# the runner stream, remember each registered value, and redact those values
# from the artifact without interpreting them as regular expressions.
: >"$log_file"
if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
  # The deployment helper writes each newly acquired mask here before it emits
  # the same control record into the captured stream. This first registration
  # never enters the artifact and protects the runner even after partial-line
  # output from an external command.
  exec 9>&1
  export SCRIBE_GITHUB_COMMAND_FD=9
else
  unset SCRIBE_GITHUB_COMMAND_FD
fi
"$@" 2>&1 | awk -v artifact="$log_file" '
  function redact_literal(value, needle, replacement, position, redacted) {
    if (needle == "") {
      return value
    }
    redacted = ""
    while ((position = index(value, needle)) != 0) {
      redacted = redacted substr(value, 1, position - 1) replacement
      value = substr(value, position + length(needle))
    }
    return redacted value
  }

  index($0, "::add-mask::") == 1 {
    mask = substr($0, length("::add-mask::") + 1)
    if (mask != "") {
      masks[++mask_count] = mask
    }
    print
    fflush()
    next
  }

  {
    print
    fflush()
    artifact_line = $0
    for (i = 1; i <= mask_count; i++) {
      artifact_line = redact_literal(artifact_line, masks[i], "***")
    }
    print artifact_line >> artifact
    fflush(artifact)
  }
'
