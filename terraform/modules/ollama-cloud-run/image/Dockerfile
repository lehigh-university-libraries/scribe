ARG OLLAMA_BASE_IMAGE=ollama/ollama:0.32.5@sha256:4dea9fb511947e24a84237bb636b0203abcb2ff0d3fbc7b4ff865deb91362131
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
RUN ollama serve & \
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
