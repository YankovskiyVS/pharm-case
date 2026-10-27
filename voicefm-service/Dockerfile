FROM python:3.11-slim-bookworm
ARG WITH_MODEL=false
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg libgomp1 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY requirements*.txt ./
RUN pip install --no-cache-dir --upgrade pip==26.2.2 setuptools==84.2.1 wheel==0.48.0 && pip install --no-cache-dir -r requirements.txt && if [ "$WITH_MODEL" = "true" ]; then pip install --no-cache-dir torch==2.11.0 torchaudio==2.11.0 --index-url https://download.pytorch.org/whl/cpu && pip install --no-cache-dir -r requirements-model.txt; fi && pip check && pip uninstall -y pip
RUN useradd -u 10001 -m app && mkdir /models && chown app:app /models
COPY . .
USER app
ENV VOICEFM_MODEL_DIR=/models HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 HF_HUB_DISABLE_TELEMETRY=1 DO_NOT_TRACK=1 CUDA_VISIBLE_DEVICES="" PYTHONDONTWRITEBYTECODE=1
EXPOSE 8002
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:8002/health/live')"
CMD ["uvicorn", "service:app", "--host", "0.0.0.0", "--port", "8002", "--no-access-log", "--log-level", "warning"]
