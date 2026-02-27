# hOCRedit (Greenfield Rewrite)

This repo now follows a split architecture:

- Go API backend (`cmd/api`, `internal/*`)
- Frontend app (`web/`) using Vite + TypeScript + Tailwind
- API contract in protobuf (`proto/`) managed with Buf
- SQL layer in `sqlc/` targeting MariaDB

## Run Backend + MariaDB

```bash
docker compose up --build
```

API will be available at `http://localhost:8080`.

Provider/model selection in the UI is driven by backend config (`/v1/llm/options`) and supports `ollama`, `openai`, and `gemini` when configured.

## Build and Lint

```bash
make lint
make test
make proto
make sqlc
make install-tools
make generate
```

`buf` and `sqlc` targets are no-ops if those binaries are not installed locally.
