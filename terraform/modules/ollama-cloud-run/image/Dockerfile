ARG OLLAMA_BASE_IMAGE=ollama/ollama:0.30.10@sha256:bfc9c6d53cc6989aa5131a6fde6b162b2802d4d337657f3253b5f69579bddeee
FROM ${OLLAMA_BASE_IMAGE}

ARG OLLAMA_MODEL

ENV \
  OLLAMA_HOST=0.0.0.0:8080 \
  OLLAMA_MODELS=/models \
  OLLAMA_DEBUG=false \
  OLLAMA_KEEP_ALIVE=-1 \
  OLLAMA_MODEL=${OLLAMA_MODEL}

RUN test -n "${OLLAMA_MODEL}"
RUN ollama serve & sleep 5 && ollama pull "${OLLAMA_MODEL}"

WORKDIR /app

COPY docker-entrypoint.sh .
RUN chmod +x /app/docker-entrypoint.sh

ENTRYPOINT ["/app/docker-entrypoint.sh"]
