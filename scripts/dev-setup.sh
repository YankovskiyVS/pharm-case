#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
umask 077
secret_dir=${SECRETS_DIR:-.local}
mkdir -p "$secret_dir"
for name in audio_token voicefm_token email_encryption_key ops_token backup_key app_db_password; do
 if [ ! -s "$secret_dir/$name" ]; then openssl rand -hex 32 > "$secret_dir/$name"; fi
done
if [ ! -s "$secret_dir/db_password" ]; then openssl rand -hex 24 > "$secret_dir/db_password"; fi
if [ ! -s "$secret_dir/seed_password" ]; then openssl rand -hex 12 > "$secret_dir/seed_password"; fi
if [ ! -s "$secret_dir/doctor_totp" ]; then
 python3 - "$secret_dir/doctor_totp" <<'PY'
import base64,secrets,pathlib,sys
pathlib.Path(sys.argv[1]).write_text(base64.b32encode(secrets.token_bytes(20)).decode().rstrip('=')+'\n')
PY
fi
# Empty by design: never fabricate or copy the coding agent's credentials.
if [ ! -f "$secret_dir/support_chat_api_key" ]; then : > "$secret_dir/support_chat_api_key"; fi
mkdir -p "$secret_dir/gmail"
mkdir -p "$secret_dir/voicefm"
if [ ! -f "$secret_dir/gmail/credentials.json" ]; then printf '{}\n' > "$secret_dir/gmail/credentials.json"; fi
printf 'Secret files ready in %s. Start with: make up\n' "$secret_dir"
