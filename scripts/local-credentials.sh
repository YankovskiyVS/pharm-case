#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
printf 'Doctor: doctor@psitech.local\nPatient: patient@psitech.local\nLocal password (both): '
password_file=$(docker compose config --format json | python3 -c 'import json,sys;print(json.load(sys.stdin)["secrets"]["seed_password"]["file"])')
cat "$password_file"
printf 'Current doctor MFA code: '
docker compose exec -T api /server totp
public_url=$(docker compose config --format json | python3 -c 'import json,sys;print(json.load(sys.stdin)["services"]["api"]["environment"]["PUBLIC_URL"].rstrip("/"))')
printf 'Patient portal: %s/patient/\nDoctor portal: %s/doctor/\n' "$public_url" "$public_url"
