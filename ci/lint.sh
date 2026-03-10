#!/usr/bin/env bash

set -euo pipefail

if command -v shellcheck >/dev/null 2>&1; then
  echo "Running ShellCheck..."
  shopt -s globstar nullglob
  shell_scripts=(**/*.sh)
  if ((${#shell_scripts[@]} > 0)); then
    shellcheck "${shell_scripts[@]}"
  fi
fi

if command -v golangci-lint >/dev/null 2>&1; then
  echo "Running golangci-lint..."
  golangci-lint run
else
  echo "golangci-lint not installed; skipping Go lint"
fi
