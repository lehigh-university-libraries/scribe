#!/usr/bin/env bash
set -euo pipefail

export SCRIBE_REPAIR_LOCAL_TOKENS=true
exec bash generate-secrets.sh
