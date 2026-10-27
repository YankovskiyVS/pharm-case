#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
umask 077
mkdir -p .local/backups
lock="$PWD/.local/backup.lock"
if ! mkdir "$lock" 2>/dev/null; then printf 'Another backup is running (.local/backup.lock).\n' >&2; exit 1; fi
stage=$(mktemp -d "$PWD/.local/backup-work.XXXXXXXX")
was_running=false
cleanup() {
 if $was_running; then docker compose start api >/dev/null; fi
 case "$stage" in "$PWD"/.local/backup-work.*) rm -rf "$stage";; esac
 rmdir "$lock" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
output=${1:-"$PWD/.local/backups/$(date -u +%Y%m%dT%H%M%SZ).psibak"}
if [[ -e "$output" || -e "$output.partial" ]]; then printf 'Backup destination already exists\n' >&2; exit 1; fi
docker compose build backup-tool >/dev/null
if [[ -n "$(docker compose ps --status running -q api)" ]]; then
 was_running=true
 docker compose stop api >/dev/null
fi
# API is stopped: no clinical writes/object uploads can race the two snapshots.
docker compose exec -T db pg_dump -U psitech -d psitech -Fc > "$stage/database.dump"
docker compose run --rm --no-deps -T backup-tool tar -C /audio -czf - . > "$stage/audio.tar.gz"
docker compose config --format json > "$stage/config.json"
python3 - "$stage" <<'PY'
import hashlib,json,pathlib,sys,tarfile
root=pathlib.Path(sys.argv[1]);config=json.loads((root/'config.json').read_text())
with tarfile.open(root/'secrets.tar.gz','w:gz') as archive:
 for name,value in config['secrets'].items():
  if name=='backup_key':continue
  source=pathlib.Path(value['file'])
  archive.add(source,arcname=name,recursive=False)
(root/'config.json').unlink()
def digest(path):
 h=hashlib.sha256()
 with path.open('rb') as stream:
  for chunk in iter(lambda:stream.read(1024*1024),b''):h.update(chunk)
 return h.hexdigest()
manifest={p.name:digest(p) for p in root.iterdir() if p.is_file()}
(root/'manifest.json').write_text(json.dumps({'version':1,'sha256':manifest},indent=2)+'\n')
PY
tar -C "$stage" -cf - . | docker compose run --rm --no-deps -T backup-tool /backup-crypt encrypt > "$output.partial"
chmod 600 "$output.partial"
mv "$output.partial" "$output"
printf 'Encrypted consistent backup: %s\n' "$output"
