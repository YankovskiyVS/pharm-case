#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
case "${1:-}" in
 '') set -- ;;
 --pilot) set -- -f compose.yaml -f compose.pilot.yaml ;;
 *) printf 'Usage: scripts/gmail-connect.sh [--pilot]\n' >&2; exit 1 ;;
esac
gmail_dir=$(docker compose "$@" config --format json | python3 -c 'import json,pathlib,sys;print(pathlib.Path(json.load(sys.stdin)["secrets"]["gmail_credentials"]["file"]).parent)')
SECRETS_DIR=$(dirname "$gmail_dir") ./scripts/dev-setup.sh
if [ ! -s "$gmail_dir/client.json" ]; then
 printf 'Save the Google OAuth Desktop client JSON to %s/client.json first. See docs/email.md.\n' "$gmail_dir" >&2
 exit 1
fi
LOCAL_UID=$(id -u) LOCAL_GID=$(id -g) docker compose "$@" run --build --rm --no-deps --service-ports gmail-authorize
printf 'Gmail authorization saved. Set GMAIL_FROM and EMAIL_ENABLED=true, then recreate api.\n'
