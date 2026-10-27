# VoiceFM research adapter and explicit demo

The default `VOICEFM_MODE=mock` is a working, deterministic PoC fixture. It validates
that uploaded bytes decode as audio, then returns **fixed synthetic scores**. Audio
content is **not used for prediction**. Its interpretation explicitly says that the
scores are demonstration data, not findings about the patient. All successful mock
responses carry `mode:mock`, `is_mock:true`, `clinical_use:false`,
`input_used_for_prediction:false`, and model `voicefm-demo-fixture-v1`.
`VOICEFM_MODE=disabled` returns503. There is no database, patient log, history lookup,
external inference API or remote model code in this service.

## Real artifact availability (checked2026-10-26)

Official source is pinned to
[86d22629bde5d4e15194197da38e9c5323f48da9](https://github.com/oelemento/VoiceFM-public/tree/86d22629bde5d4e15194197da38e9c5323f48da9).
Code is MIT; that license does not establish a license to clinical weights or data.
[Upstream access instructions](https://github.com/oelemento/VoiceFM-public/blob/86d22629bde5d4e15194197da38e9c5323f48da9/data/README.md)
refer to a separate checkpoint DOI without providing it.
[The paper](https://www.medrxiv.org/content/10.64898/2026.05.28.26354346v1.full)
provides dataset access and source code, but no trained-weight download in its
availability sections. Official GitHub releases, Hugging Face VoiceFM model search
and Zenodo VoiceFM query returned no artifacts. Exact endpoints/findings are recorded
in [availability.json](availability.json). This is evidence of no discoverable public
release, not proof that an authorized private release does not exist.

The legitimate next step is to request the checkpoint DOI/access and license from
the corresponding authors or open an upstream issue **with the operator's approval**.
Bridge2AI-Voice data access separately requires PhysioNet credentials and a signed
DUA. Do not confuse its dataset DOI with a model checkpoint. No account was created,
DUA accepted, author contacted or issue posted by this implementation.

Also needed: fitted clinical probes and scaler parameters, and original recording
task mappings. The released probe script trains StandardScaler and logistic
regression on participant-mean embeddings; released AUROC tables are evaluation
results, not usable classifiers. They are never returned as predictions here.
The upstream `whisper_extract_embeddings_v3.py` script extracts the frozen Whisper
baseline; it is not substituted for the clinical contrastive VoiceFM checkpoint.

## API and Compose contract

Private port8002. Bearer token from `VOICEFM_TOKEN_FILE` (minimum32 characters).
`GET /health/live` is public liveness; `GET /health/ready` returns200 only if the
selected mode is usable, always with explicit mode. `GET /status` requires the token.
`POST /analyze` (alias `/infer`) accepts raw audio in its original Content-Type and
optional `X-Voice-Task: vowel|reading|free_speech|unknown`. Successful envelope:

```json
{"status":"ready","mode":"mock","is_mock":true,"clinical_use":false,
 "input_used_for_prediction":false,"research_only":true,
 "model":{"name":"voicefm-demo-fixture-v1","revision":"fixture-v1-fixed-scores"},
 "outputs":{"scores":[{"label":"cat_voice","score":0.25}],
 "interpretation":"Synthetic demonstration fixture, not a prediction about this patient."},
 "limitations":["Synthetic demonstration only; no clinical use."]}
```

Real mode has the same envelope and `is_mock:false`. Missing/incompatible artifacts
return503 with `status:model_unavailable`, emptyoutputs and a precise `reason_code`:
`not_installed`, `probe_missing`, `artifact_hash_mismatch`, `unsupported_checkpoint`,
`probe_invalid`, `task_map_invalid`, `runtime_not_installed`. Transient timeout is503;
queue saturation429; invalid audio422. Missing token401. No placeholder real scores.

Body limit12MiB; duration0.025–120s; oneactive request and queuewait3s(max10).
`VOICEFM_TIMEOUT_SECONDS` defaults90s(max180). Each real operation is an isolated
CPU process group killed on timeout/cancellation. Temporary audio/results are deleted.
A real process loads the complete model for each operation; this trades latency for
bounded lifetime and failure isolation. No request downloads anything.

Default Docker build is light and works without Torch or weights. Build with
`--build-arg WITH_MODEL=true` only to enable the real CPU runtime. A 637M-parameter
FP32 Whisper encoder alone uses approximately2.6GB; allow8GBcontainer memory and
adequate Docker Desktop resources. `VOICEFM_MODEL_DIR=/models` is mounted read-only;
only `/tmp` needs writeaccess (allow512MiB). Check model-specific performance before
raising Go/API timeouts or enabling real mode. Rollback: set `VOICEFM_MODE=mock` or
`disabled`, rebuild light image if desired, and recreate the service.

## Authorized real bundle

Only a complete **trained VoiceFM contrastive** checkpoint is supported, not frozen
Whisper, HuBERT, HeAR, supervised multitask heads or arbitrarypickle. Architecture
and forward match pinned upstream `src/models/whisper_encoder.py`: Whisperlarge-v2
encoder32layers/1280dim/80mels, meanpool, recording-task embedding, learned
1280→1280→256 projection with GELU/LayerNorm and L2normalization. All tensor keys and
shapes load strictly; no initialized tensor survives partial loading. Original local
Whisper configuration and feature extractor configuration are required. The adapter
uses the upstream -40dB silence threshold, unitpeak normalization, mono16kHz sinc
resampling and30s maximum crop. Single-recording pooling corresponds to upstream
batch_size1; the upstream probe batching affects masks. No equivalence/clinical
quality claim is made without an original checkpoint and reference inference data.

Required files:

- `weights.safetensors`: complete audio encoder state with local keys `encoder.*`,
  `task_embedding.*`, `projection.*`; never executablepickle.
- `config.json`, `preprocessor_config.json`: original Whisperlarge-v2 configs.
- `task_map.json`: exact verified application task aliases to training IDs,
  including `unknown:0`; never infer IDs by alphabetical sorting a new dataset.
- `probes.json`: original fitted StandardScaler mean/scale(length256) and binary
  logistic coefficients/intercepts for only released supported labels.
- `manifest.json`: schema described by `artifacts.Manifest`; architecture
  `voicefm-whisper-large-v2-contrastive-256`, pinned `upstream_revision`, actual HTTPS
  `source_url`, `weights_license`, `probe_source`, `probe_license`, `research_only:true`,
  `schema_version:1`, and `files` containing all five filenames and exactSHA256bytes.

Probes schema (operator supplies real fitted arrays, no examples masquerading as a model):
`{schema_version:1,aggregation:"participant_mean",scaler_mean:[256floats],
scaler_scale:[256positivefloats],probes:[{label,coef:[256floats],intercept}]}`.
Supported labels are the14 upstream GSDcategory/diagnosislabels in `artifacts.LABELS`.
Published participant-level research probes are not calibrated for individual
recordings, Russian speech or this application's patients; every result says so.

```sh
# Offline, operator-authorized artifacts only. Verify does not claim model readiness.
python -m manage_models verify /path/to/authorized-bundle
python -m manage_models install --source /path/to/authorized-bundle --destination /models
# Optional restricted conversion in the model-enabled image:
python -m manage_models convert-checkpoint --source /path/to/best_model.pt --output weights.safetensors
```

Conversion uses `torch.load(weights_only=True)` and never falls back to unsafe
unpickling or allowlists custom globals. If a genuine checkpoint is rejected, ask
its producer for a safe state-only/safetensors export. Hash verification establishes
integrity, not authorization; the operator must verify origin and license. Install
refuses to overwrite existing models; restart the service after replacing a bundle.
Real readiness requires successful subprocess construction and strict complete load,
not just files existing. No real-model smoke was possible because trained artifacts
were unavailable; tests cover fixture behavior, authentication, limits, provenance,
malformed bundles and failure modes without creating fake clinical weights.
