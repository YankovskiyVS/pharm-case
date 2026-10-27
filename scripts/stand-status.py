#!/usr/bin/env python3
"""Report redacted operational state without credentials or clinical data."""
import json
import os
import pathlib
import ssl
import subprocess
import sys
import urllib.error
import urllib.request

repo = pathlib.Path(__file__).resolve().parent.parent
os.chdir(repo)
compose = ["docker", "compose"]
if "--pilot" in sys.argv:
    compose += ["-f", "compose.yaml", "-f", "compose.pilot.yaml"]
config = json.loads(subprocess.check_output(compose + ["config", "--format", "json"]))
origin = config["services"]["api"]["environment"]["PUBLIC_URL"].rstrip("/")
secret = pathlib.Path(config["secrets"]["ops_token"]["file"]).read_text().strip()
context = None
if origin.startswith("https://"):
    context = ssl.create_default_context(cafile=str(repo / ".local/tls/pilot-ca.pem"))
request = urllib.request.Request(
    origin + "/api/v1/ops/status", headers={"Authorization": "Bearer " + secret}
)
try:
    with urllib.request.urlopen(request, timeout=15, context=context) as response:
        status = json.load(response)
except (urllib.error.URLError, ValueError) as error:
    code = " HTTP " + str(error.code) if isinstance(error, urllib.error.HTTPError) else ""
    print("Stand operations check failed: " + type(error).__name__ + code, file=sys.stderr)
    sys.exit(1)

# Model stages and cache counters only; no transcript payloads.
probe = """
import json, urllib.error, urllib.request
try:
    with urllib.request.urlopen('http://localhost:8001/health/ready', timeout=5) as response:
        result = json.load(response)
except urllib.error.HTTPError as error:
    result = json.load(error).get('detail', {'ready': False})
print(json.dumps(result))
"""
result = subprocess.run(
    compose + ["exec", "-T", "audio", "python", "-c", probe],
    capture_output=True, text=True, timeout=15,
)
try:
    status["audio"] = json.loads(result.stdout) if result.returncode == 0 else {"ready": False}
except ValueError:
    status["audio"] = {"ready": False}
voicefm_probe = """
import json, os, pathlib, urllib.error, urllib.request
token = pathlib.Path(os.environ['VOICEFM_TOKEN_FILE']).read_text().strip()
request = urllib.request.Request('http://localhost:8002/status', headers={'Authorization': 'Bearer ' + token})
try:
    with urllib.request.urlopen(request, timeout=5) as response:
        result = json.load(response)
except urllib.error.HTTPError as error:
    result = json.load(error)
print(json.dumps(result))
"""
voicefm = subprocess.run(
    compose + ["exec", "-T", "voicefm", "python", "-c", voicefm_probe],
    capture_output=True, text=True, timeout=15,
)
try:
    status["voicefm"] = json.loads(voicefm.stdout) if voicefm.returncode == 0 else {"ready": False, "reason_code": "service_unavailable"}
except ValueError:
    status["voicefm"] = {"ready": False, "reason_code": "invalid_status"}
print(json.dumps(status, ensure_ascii=False, indent=2))
if "--strict" in sys.argv:
    if status.get("status") != "ok":
        print("Operational exceptions require attention.", file=sys.stderr)
        sys.exit(1)
    support = status.get("support_chat", {})
    if not support or (support.get("mode") != "disabled" and (not support.get("ready") or not support.get("agent", {}).get("enabled"))):
        print("The configured support agent is not ready; inspect its provider and agent capabilities.", file=sys.stderr)
        sys.exit(1)
    if not status["audio"].get("ready"):
        print("Models are not ready. Run make models, then make verify.", file=sys.stderr)
        sys.exit(1)
    if config["services"]["api"]["environment"].get("VOICEFM_MODE", "mock") != "disabled" and not status["voicefm"].get("ready"):
        print("VoiceFM adapter is not ready; inspect its mode and reason_code.", file=sys.stderr)
        sys.exit(1)
if "--require-real-voicefm" in sys.argv:
    if not status["voicefm"].get("ready") or status["voicefm"].get("mode") != "real" or status["voicefm"].get("is_mock") is not False:
        print("A real loaded VoiceFM model is required; a mock does not satisfy this check.", file=sys.stderr)
        sys.exit(1)
