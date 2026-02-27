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

## Build Frontend Assets

Frontend assets are built only inside Docker:

```bash
make web-build
```

## Build and Lint

```bash
make lint
make test
make proto
make sqlc
make web-build
make install-tools
make generate
```

`buf` and `sqlc` targets are no-ops if those binaries are not installed locally.
