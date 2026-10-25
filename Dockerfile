FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# models first (1.7 GB): downloaded once, docker caches this layer across code updates
COPY tools/download_models.py tools/
RUN python tools/download_models.py
COPY requirements.txt .
# playwright is only for UI tests; the GPU build of ONNX Runtime exists for x86_64 only (ARM / Apple Silicon: CPU)
RUN grep -v '^playwright' requirements.txt > /tmp/req.txt \
 && if [ "$(uname -m)" != x86_64 ]; then sed -i 's/^onnxruntime-gpu.*/onnxruntime/' /tmp/req.txt; fi \
 && pip install --no-cache-dir -r /tmp/req.txt
COPY . .
ENV PS_DB=/data/faces.db HOME=/data/home PS_SCAN_SRC=/photos PS_MEDIA_BIND=0.0.0.0:8502 TQDM_MININTERVAL=60
EXPOSE 8501 8502
CMD ["sh", "/app/docker-start.sh"]
