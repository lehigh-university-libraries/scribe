ARG OLLAMA_BASE_IMAGE=ollama/ollama:0.19.0@sha256:bf240c2847a8bc7b2c630b85dab5d1dedcba257b551d5fc9b290ce544d59272a
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
