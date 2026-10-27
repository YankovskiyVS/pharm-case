#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
umask 077
if [[ $# != 1 || ! -f "$1" ]]; then printf 'Usage: scripts/restore-check.sh backup.psibak\n' >&2; exit 1; fi
stage=$(mktemp -d "$PWD/.local/restore-work.XXXXXXXX")
project="psitech-restore-$(date +%s)-$$"
cleanup(){ docker compose -p "$project" -f infra/restore/compose.yaml down --volumes >/dev/null 2>&1 || true; case "$stage" in "$PWD"/.local/restore-work.*) rm -rf "$stage";; esac; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# Do not unpack until authentication of the complete encrypted stream succeeds.
# Keep build output outside the binary plaintext stream.
docker compose build backup-tool >/dev/null
docker compose run --rm --no-deps -T backup-tool /backup-crypt decrypt < "$1" > "$stage/plain.tar"
python3 - "$stage" <<'PY'
import hashlib,json,pathlib,sys,tarfile
root=pathlib.Path(sys.argv[1])
with tarfile.open(root/'plain.tar') as archive:
 for member in archive.getmembers():
  target=(root/member.name).resolve()
  if not target.is_relative_to(root.resolve()) or not (member.isfile() or member.isdir()):raise SystemExit('unsafe backup member')
 archive.extractall(root)
manifest=json.loads((root/'manifest.json').read_text())
if manifest['version']!=1:raise SystemExit('unsupported backup version')
for name,digest in manifest['sha256'].items():
 if pathlib.Path(name).name!=name:raise SystemExit('unsafe manifest name')
 h=hashlib.sha256()
 with (root/name).open('rb') as stream:
  for block in iter(lambda:stream.read(1024*1024),b''):h.update(block)
 if h.hexdigest()!=digest:raise SystemExit('backup integrity check failed')
PY
docker compose -p "$project" -f infra/restore/compose.yaml up -d --wait >/dev/null
docker compose -p "$project" -f infra/restore/compose.yaml exec -T db pg_restore -U restore_check -d restore_check --no-owner --no-privileges --exit-on-error < "$stage/database.dump"
docker compose -p "$project" -f infra/restore/compose.yaml exec -T db psql -U restore_check -d restore_check -At -c "SELECT object_key || ' ' || sha256 FROM audio_recordings" > "$stage/objects.txt"
python3 - "$stage" <<'PY'
import hashlib,pathlib,sys,tarfile
root=pathlib.Path(sys.argv[1]);expected={}
for line in (root/'objects.txt').read_text().splitlines():
 name,digest=line.split();expected[name]=digest
with tarfile.open(root/'audio.tar.gz') as archive:
 seen=set()
 for member in archive:
  name=member.name.removeprefix('./')
  if member.issym() or member.islnk():raise SystemExit('unsafe audio member')
  if name not in expected:continue
  source=archive.extractfile(member)
  if source is None:raise SystemExit('missing audio bytes')
  h=hashlib.sha256()
  for block in iter(lambda:source.read(1024*1024),b''):h.update(block)
  digest=h.hexdigest()
  if digest!=expected[name]:raise SystemExit('audio/database checksum mismatch')
  seen.add(name)
 if seen!=set(expected):raise SystemExit('database references missing audio objects')
with tarfile.open(root/'secrets.tar.gz') as secrets:
 names={m.name for m in secrets.getmembers() if m.isfile()}
 required={'db_password','app_db_password','email_encryption_key','audio_token','ops_token','voicefm_token'}
 if not required<=names:raise SystemExit('required runtime secrets missing')
print(f'Restore verified: database restored; {len(expected)} audio objects matched; runtime secrets present. Live data unchanged.')
PY
