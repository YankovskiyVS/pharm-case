# ПсиTech — local first iteration

Separate doctor and patient web applications, a Go API and PostgreSQL. Russian UI.
Local voice analysis and transcript retrieval are available through the audio extension.
Agents and external push/SMS remain deferred. Email is the secondary notification channel.
This local setup contains **synthetic data only** and binds only to localhost.

## Start

Prerequisites: Docker Desktop / Docker Engine with Compose v2, OpenSSL and Python 3
(for first-run local secret generation). Node and Go are unnecessary to run Docker.

```sh
make up
make credentials
```

`make up` builds the services, runs migrations, downloads/prewarms the public
ASR and embedding models, and checks readiness. First start requires internet
for images/dependencies/model weights; subsequent inference uses cached local files.
Use `make up WITH_MODELS=0` for a quick core/acoustics-only stand; transcription
and semantic search then report model readiness explicitly.

- Patient: **http://localhost:8080/patient/**
- Doctor/admin: **http://localhost:8080/doctor/**
- `http://localhost:8080/` opens the patient portal.

The credential helper prints the local password and the **current six-digit doctor
MFA code**. Codes change every 30 seconds; rerun the helper when a code expires.
The doctor secret can also be added to a TOTP authenticator from `.local/doctor_totp`.

| Role | Login |
|---|---|
| Doctor | `doctor@psitech.local` |
| Patient | `patient@psitech.local` |
| Other synthetic patients | `patient2@psitech.local` through `patient15@psitech.local` |

All seeded accounts use the generated local password. The password and database/
TOTP secrets are randomly generated in ignored `.local/` files, mounted as Compose
secrets, never baked into images. Do not delete/rotate these files independently of
the existing database: seeding is intentionally one-time and does not reset accounts.
The doctor's demo cohort is reconciled to exactly 15 active synthetic patients with
varied diary adherence, EMA trajectories, sleep patterns, review recency and alerts.

First build downloads base images and dependencies. Subsequent starts reuse them.
Use `APP_PORT=8081 docker compose up --build -d --wait db api frontend audio` if port 8080 is occupied;
use that port in browser URLs. App origins adapt to APP_PORT. No DB/API ports are
published. Compose waits for database and API readiness before serving the UIs.

## Try the product

1. Open the doctor portal and log in using the password and MFA code from the helper.
2. Open a patient profile. Inspect EMA trends, sleep history, schedules and chat.
3. In another browser/incognito window, log into the patient portal. Both portals
   deliberately share a same-origin session, so use separate browser contexts to
   test two identities simultaneously.
4. Complete today's sleep diary and EMA. Selecting **Да** for suicidal thoughts
   persists a risk signal immediately, before the rest of the EMA is submitted.
5. In the doctor portal, open **Сигналы**. Acknowledge the alert, then record a
   follow-up action to close it. Corrections to diaries do not erase prior alerts.
6. Exchange messages. Use **Дневники** in the patient UI to append corrections.
7. To add a patient, create an invitation in the doctor portal and open the generated
   link in a separate/private browser session. Accept with a new password and local
   consent. Back in the doctor portal assign CSD/EMA dates under **Расписание**.

Notifications persist in-app. Gmail API is the only email provider; sending stays
disabled until a sender exists and Google OAuth is completed. See
[Gmail setup](docs/email.md) and `make gmail-connect`.
SSE refresh hints have a periodic polling fallback while the portal is open. The five-minute overdue flag
means “not yet acknowledged”, not “SMS/call escalation delivered”. Gmail acceptance does not confirm clinician response. No unattended
external emergency-response service is connected. The weekly questionnaire remains separate from clinical review. Verified external
scale sources and the MADRS/MADRS-S distinction are recorded in
[clinical instrument sources](docs/clinical-instruments.md); full scale collection
is not yet integrated.

## Stop, inspect and update

```sh
docker compose logs --tail=100
docker compose ps
docker compose down                      # keeps PostgreSQL data
# After changing code:
docker compose up --build -d --wait db api frontend audio
```

A one-shot `migrate` container runs SQL under a database advisory lock and
provisions the restricted `psitech_app` runtime role. The API has no schema-creation
privileges and cannot update/delete immutable history tables. Synthetic seeding
runs once. PostgreSQL uses a named volume, so diary/chat data survives recreation.
`docker compose down --volumes` is a **destructive local reset**; use only when you
want to delete all local records. Keep `.local/` if you want the same credentials
for a freshly created database.

## Verification

Backend unit, race and real PostgreSQL integration tests (isolated disposable DB):

```sh
docker compose run --build --rm api-test
```

The test profile uses a separate `test-db`; integration tests also isolate their
schemas. These tests never point at the interactive application's database. Stop
the test DB afterwards with `docker compose --profile test stop test-db`.

Frontend build/type checks and lint (Node 22.12+):

```sh
cd frontend-app
npm ci --include=dev
npm run generate
npm run typecheck
npm run lint
npm run format:check
npm run build
```

Browser tests against the running Compose stack:

```sh
cd frontend-app
npx playwright install chromium
npm run test:e2e
```

Browser tests create uniquely named synthetic patients in the **local app database**.
Do not run them against a real clinic deployment. Override the local URL with
`E2E_BASE_URL=http://localhost:8081` when changing APP_PORT. Test traces/screenshots
stay in ignored `frontend-app/test-results/`.

Native Go checks (Go 1.27): `go vet ./...` and `go test -race -cover ./...` from
`backend-app`. Database integration tests are skipped without TEST_DATABASE_URL;
the Compose test command supplies it. Scoring tests require no database.

See [email configuration and delivery semantics](docs/email.md),
[direct database doctor provisioning](docs/doctor-provisioning.md),
[form/source reconciliation](docs/forms-audit.md), and [code review](docs/code-review.md).

## Repository

- `frontend-app/apps/doctor`, `frontend-app/apps/patient`: independent React bundles.
- `frontend-app/packages`: shared UI primitives and generated API schema types.
- `backend-app/internal/app`: identity, care, diaries, alerts, chat and durable events.
- `backend-app/internal/scoring`: pure Excel-equivalent CSD/EMA calculations.
- `backend-app/internal/store`: pool setup and embedded versioned SQL migrations.
- `api/openapi.json`: REST contract; `npm run generate` refreshes TS definitions.
- [Architecture](docs/architecture.md), [API](docs/api.md), [data model](docs/data-model.md),
  [security and local deployment boundary](docs/security.md).

Runtime images use non-root users and multi-stage builds. DB/API remain on the
private Compose network. Real clinical deployment requires production hosting,
key management, retention/consent review, monitored backup restoration,
operational coverage and approved external notification channels. The local HTTPS pilot and encrypted backup checks below cover development validation;
production hosting and operational coverage remain separate.

## Audio analysis and transcript retrieval

The patient portal has a **Голос** tab: consent, a repeatable reading task, a free
response or a sustained vowel, browser recording or file upload, then background
analysis. The doctor profile has a **Голос** tab with private playback, timestamped
transcription, acoustic measurements, compatible-recording deltas and a pitch trend.
Recording requires localhost or HTTPS and microphone permission. Uploads are limited
to 12 MiB, 2–120 seconds and 20 recordings per patient per rolling day. For reliable
comparison aim for at least 5 seconds, the same task and the same declared device.
No audio or transcription is stored in browser localStorage.

The private `audio` Python service uses Praat/Parselmouth, bundled Silero VAD and
CPU/int8 faster-whisper `base` (Russian). Raw digital gain is preserved when
extracting features. Jitter/shimmer are measured only for sustained vowels. Noise,
recording conditions and other speakers can distort results. No acoustic diagnosis,
emotion probability or new clinical risk rule is implemented.

Local multilingual MiniLM embeddings plus independent BM25, rank fusion and
deterministic reranking retrieve transcript quotations with recording/time citations.
The prototype searches a bounded authorized set of recent chunks and caches
embeddings in a persistent SQLite store keyed by model fingerprint and chunk content.
It does not yet deploy pgvector or a clinic-wide document index. Its evidence threshold
is a technical starting point, not a validated clinical relevance threshold. There
is no external generative model call or clinical-guideline corpus. Approved clinical
documents and a representative labelled Russian evaluation set are still needed.

`make models` downloads public model weights into `audio_models` and runs warm
inference before declaring readiness. Runtime model loading uses local files only.
Patient content is processed locally, not sent to a model provider. Liveness
(`/health/live`) and actual model readiness (`/health/ready`) are distinct. Acoustic results survive an explicit ASR failure; retry from the
recording card after model availability is restored.

`audio_data` stores original private recordings, PostgreSQL stores metadata/jobs/
versioned results, `audio_models` stores model weights. Back up the first two together.
A crash between storing an object and committing its metadata can leave a private
orphan; reconcile old objects against `audio_recordings.object_key` before cleanup.
Do not delete unreferenced objects concurrently with uploads. Deletion/retention
policy and shared S3 storage require an operator decision for deployment beyond
this single-host prototype.

B2AI-Voice 3.0.0 is a restricted research source, not bundled training data. Its
PhysioNet release has derived features, not raw audio. No B2AI data has been downloaded
or used to train a model. See `docs/adr/0006-audio-rag.md`.

Validation commands (requires Docker and model/dependency downloads):

```sh
docker compose run --build --rm api-test
docker compose run --build --rm audio python -m pytest -q
docker compose run --rm -e RUN_MODEL_SMOKE=1 audio python -m pytest -q tests/test_model_smoke.py
docker compose run --rm audio sh -c 'python -m mypy --strict *.py'
docker compose run --rm audio python -m black --check .
docker compose run --rm audio python -m ruff check .
cd frontend-app && npm run test:e2e -- tests/audio.spec.ts
```

Synthetic-signal tests verify extraction behavior; API integration tests use a fake
ML adapter to verify access, persistence, retries and fencing. These do not establish
clinical validity or ASR quality. The real-model smoke test uses synthetic Russian speech to verify transcription,
timestamps and cited retrieval; this does not measure representative clinical accuracy.

## Local HTTPS pilot and operations

`make pilot-up` starts a separate `psitech-pilot` Compose project at
**https://localhost:8443** with secure cookies, separate secrets/volumes and synthetic
seeding disabled. Caddy uses an internal CA;
its public root certificate is exported to `.local/tls/pilot-ca.pem`. Trust that
certificate explicitly for browser testing; the script never changes your OS trust
store. Provision doctors directly in the pilot DB using
[the operator guide](docs/doctor-provisioning.md). This remains a local test stand,
not an authorization to use real medical data. `make pilot-down` preserves volumes.

`make status` reports redacted job/mail/risk/model status through the protected
operator endpoint. `make verify` additionally requires both model lanes ready.
The operator token is generated in `.local/ops_token`; it is never shown in URLs.
See [stand operations](docs/operations.md) for backup/restore and Gmail failure handling.

```sh
make backup                         # stops API briefly for a consistent snapshot
make restore-check BACKUP=.local/backups/<timestamp>.psibak
```

Snapshots contain the database, original audio and runtime secrets, encrypted under
`.local/backup_key`. Keep that key separately: it is intentionally excluded from
the archive. Restore verification uses an isolated temporary PostgreSQL instance
and matches audio bytes against restored database hashes; live data is untouched.
The helper is a local recovery tool; retention, off-host storage and PITR remain
operator choices. `make test` runs backend/audio checks and browser tests against
the running synthetic stand. CI configuration is in `.github/workflows/ci.yaml`.

## Clinical audio presentation PoC

The doctor portal now has a **Клинический контекст** tab with versioned history,
medication descriptions, confounders and sourced external scale totals. Each audio
recording can produce a source-bound clinical report and separate authored doctor
conclusions. Private clinical context is not exposed to the patient portal.

The default `voicefm` adapter runs **mock** mode: its model scores are a fixed
synthetic presentation fixture, not predictions about the patient. Actual acoustics,
transcription and stored clinical facts stay separate. Mode/provenance are explicit
in the UI and saved report. `mock`, `real` and `disabled` share one adapter contract;
real mode requires authorized trained artifacts and never falls back to mock.

Use `make up` and follow [the demonstration guide](docs/clinical-audio.md).
[Artifact access findings](voicefm-service/availability.json) and the
[real bundle specification](voicefm-service/README.md) describe how to replace the
fixture. Public code was found; a usable authorized weight/probe bundle was not.

## Patient support chat

The patient portal has a separate **Поддержка** agent with persistent dialogue state,
patient-bound tools for today's tasks and diary explanations, and suggested support
actions. A bounded observe/act loop validates each tool before execution. By the user's choice it starts
in an explicitly labeled **demo** mode with a deterministic planner using the real
tool executor. OpenAI Responses function-calling support is
prepared for a project API key; Codex session credentials are not reused. See
[setup and demonstration](docs/support-chat.md).
