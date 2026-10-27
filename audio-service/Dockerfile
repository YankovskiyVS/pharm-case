# Praat/Parselmouth does not publish wheels for every CPU architecture.
# Compile its wheel on the target architecture, keeping toolchains out of runtime.
FROM python:3.11-slim-bookworm AS wheel
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake ninja-build && rm -rf /var/lib/apt/lists/*
ENV CMAKE_BUILD_PARALLEL_LEVEL=2 CMAKE_ARGS="-DCMAKE_POLICY_VERSION_MINIMUM=3.5"
RUN pip wheel --no-cache-dir --no-deps --wheel-dir /wheels praat-parselmouth==0.4.6

FROM python:3.11-slim-bookworm AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg libgomp1 espeak-ng && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY requirements.txt .
COPY --from=wheel /wheels /wheels
# pip is build tooling: its bundled HTTP/msgpack dependencies are not needed by inference.
RUN pip install --no-cache-dir --upgrade pip==26.2.2 setuptools==84.2.1 wheel==0.48.0 && pip install --no-cache-dir --find-links=/wheels -r requirements.txt && pip check && pip uninstall -y pip && rm -rf /wheels
RUN useradd -u 10001 -m app && mkdir -p /models /cache && chown app:app /models /cache
COPY . .
USER app
ENV HF_HOME=/models/huggingface AUDIO_MODEL_DIR=/models AUDIO_CACHE_DIR=/cache
ENV AUDIO_MODEL=base AUDIO_EMBED_MODEL=sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2
ENV PYTHONDONTWRITEBYTECODE=1 TOKENIZERS_PARALLELISM=false AUDIO_PREWARM_ON_START=true HF_HUB_DISABLE_TELEMETRY=1
EXPOSE 8001
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:8001/health/live')"
CMD ["uvicorn", "service:app", "--host", "0.0.0.0", "--port", "8001", "--no-access-log", "--log-level", "warning"]
