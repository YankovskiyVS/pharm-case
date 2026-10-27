#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
i=0
while [ "$i" -lt 60 ]; do
 if docker compose "$@" exec -T audio python -c "import urllib.request; urllib.request.urlopen('http://localhost:8001/health/ready',timeout=2)" >/dev/null 2>&1; then printf 'Audio and retrieval models ready.\n'; exit 0; fi
 i=$((i+1)); sleep 2
done
printf 'Models are not ready; inspect make status and docker compose logs audio.\n' >&2
exit 1
