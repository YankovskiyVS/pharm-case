#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
SECRETS_DIR=${PILOT_SECRETS_DIR:-.local/pilot} ./scripts/dev-setup.sh
docker compose -f compose.yaml -f compose.pilot.yaml up --build -d --wait --remove-orphans db audio voicefm api frontend gateway
docker compose -f compose.yaml -f compose.pilot.yaml run --build --rm models
docker compose -f compose.yaml -f compose.pilot.yaml restart audio
./scripts/wait-models.sh -f compose.yaml -f compose.pilot.yaml
mkdir -p .local/tls
docker compose -f compose.yaml -f compose.pilot.yaml cp gateway:/data/caddy/pki/authorities/local/root.crt .local/tls/pilot-ca.pem
printf 'HTTPS stand: https://localhost:%s\nCA: .local/tls/pilot-ca.pem\nNo demo accounts; provision doctors directly in the pilot database. Gmail stays disabled until explicitly configured.\n' "${HTTPS_PORT:-8443}"
