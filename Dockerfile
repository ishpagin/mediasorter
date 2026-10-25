# 1. the web UI (Vue): built here, embedded into the Go binary
FROM node:22-slim AS ui
WORKDIR /web/frontend
COPY web/frontend/package.json web/frontend/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/frontend/ ./
RUN npm run build

# 2. the web server (Go, no cgo): one static binary with the UI inside
FROM golang:1.27 AS server
WORKDIR /web
COPY web/go.mod web/go.sum ./
RUN go mod download
COPY web/ ./
COPY --from=ui /web/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mediasorter-web ./cmd/mediasorter-web

# 3. the analysis (Python: faces, scenes, scan) + the web server
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
COPY --from=server /mediasorter-web /usr/local/bin/mediasorter-web
COPY . .
ENV PS_DB=/data/faces.db HOME=/data/home PS_SCAN_SRC=/photos TQDM_MININTERVAL=60 \
    MS_WEB_ADDR=:8501 MS_ML_URL=http://127.0.0.1:8503 MS_PROTECTED=/app/protected.txt
EXPOSE 8501
CMD ["sh", "/app/docker-start.sh"]
