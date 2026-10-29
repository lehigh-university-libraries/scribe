ARG OLLAMA_BASE_IMAGE=ollama/ollama:0.40.13@sha256:b2f0122c476bc9fe7ce679d8bb4bd555351e25e2554eefb156f61cb0a89cf429
FROM ${OLLAMA_BASE_IMAGE}

ARG OLLAMA_MODEL
ARG OLLAMA_MODEL_DIGEST

ENV \
  OLLAMA_HOST=0.0.0.0:8080 \
  OLLAMA_MODELS=/models \
  OLLAMA_DEBUG=false \
  OLLAMA_KEEP_ALIVE=-1 \
  OLLAMA_MODEL=${OLLAMA_MODEL}

RUN test -n "${OLLAMA_MODEL}" && printf '%s' "${OLLAMA_MODEL_DIGEST}" | grep -Eq '^[0-9a-f]{64}$'
RUN set -eu; \
    ollama serve & \
    server_pid=$!; \
    trap 'kill "$server_pid" 2>/dev/null || true' EXIT; \
    sleep 5; \
    ollama pull "${OLLAMA_MODEL}"; \
    actual_digest="$(ollama list | awk -v model="${OLLAMA_MODEL}" 'NR > 1 && $1 == model { print $2; exit }')"; \
    test -n "$actual_digest"; \
    case "${OLLAMA_MODEL_DIGEST}" in "${actual_digest}"*) ;; *) echo "Ollama model digest mismatch: expected ${OLLAMA_MODEL_DIGEST}, got ${actual_digest}" >&2; exit 1 ;; esac

WORKDIR /app

COPY docker-entrypoint.sh .
RUN chmod +x /app/docker-entrypoint.sh

ENTRYPOINT ["/app/docker-entrypoint.sh"]
