#!/usr/bin/env bash

set -euo pipefail

{
    until ollama list > /dev/null 2>&1; do sleep 1; done
    ollama run "$OLLAMA_MODEL" ""
} &

exec ollama serve
