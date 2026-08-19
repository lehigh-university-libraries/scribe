#!/usr/bin/env bash
set -euo pipefail

exec curl --noproxy '*' --fail --silent --show-error --connect-timeout 2 --max-time 10 \
  --output /dev/null http://127.0.0.1/readyz
