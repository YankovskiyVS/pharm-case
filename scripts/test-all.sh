#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
./scripts/dev-setup.sh
docker compose run --build --rm api-test
docker compose run --build --rm --no-deps audio python -m pytest -q
docker compose run --rm --no-deps audio sh -c 'python -m mypy --strict *.py'
docker compose run --rm --no-deps audio python -m black --check .
docker compose run --rm --no-deps audio python -m ruff check .
docker compose run --build --rm --no-deps voicefm python -m pytest -q
docker compose run --rm --no-deps voicefm sh -c 'python -m mypy --strict *.py && python -m black --check . && python -m ruff check .'
(cd frontend-app && npm ci --include=dev && npm run generate && npm run typecheck && npm run lint && npm run format:check && npm run build && npm run test:e2e)
