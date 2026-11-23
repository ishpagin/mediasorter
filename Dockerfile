# Mediasorter server: one Go program (web UI + analysis), ONNX Runtime, ffmpeg and the models.
# CPU by default; docker-compose.gpu.yml builds the NVIDIA (CUDA) variant with BASE and ORT.
ARG BASE=debian:trixie-slim

# 1. the web UI (Vue): built here, embedded into the Go binary
FROM node:22-slim AS ui
WORKDIR /web/frontend
COPY web/frontend/package.json web/frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-fund --no-audit
COPY web/frontend/ ./
RUN npm run build

# 2. the server (Go + cgo for ONNX Runtime)
FROM golang:1.27 AS server
WORKDIR /web
COPY web/go.mod web/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY web/ ./
COPY --from=ui /web/frontend/dist ./frontend/dist
# cache mounts: a code change recompiles only the changed packages (BuildKit)
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /mediasorter-web ./cmd/mediasorter-web

# 3. the models (~1.7 GB): downloaded once, docker caches this layer across code updates
FROM debian:trixie-slim AS models
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip && rm -rf /var/lib/apt/lists/*
WORKDIR /models
RUN curl -sSLf --retry 5 -o b.zip https://github.com/deepinsight/insightface/releases/download/v0.7/buffalo_l.zip \
 && unzip -j b.zip "*det_10g.onnx" "*w600k_r50.onnx" && rm b.zip \
 && for f in config.json visual/model.onnx visual/preprocess_cfg.json textual/model.onnx textual/tokenizer.json \
             textual/tokenizer_config.json textual/special_tokens_map.json; do \
      curl -sSLf --retry 5 --create-dirs -o "siglip2/$f" "https://huggingface.co/immich-app/ViT-B-16-SigLIP2__webli/resolve/main/$f"; \
    done \
 && chmod -R a+rX /models  # readable when the container runs as a normal user (PUID)

# 4. what runs
FROM ${BASE}
ARG ORT=onnxruntime-linux-x64-1.24.4
# the same libraries as the GitHub release tarballs, from NuGet: github.com/microsoft/onnxruntime went 404 (2026-11-23);
# ORT only picks GPU (a name with "gpu") or CPU
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip ffmpeg tzdata \
 && case "$ORT" in *gpu*) pkg=microsoft.ml.onnxruntime.gpu.linux ;; *) pkg=microsoft.ml.onnxruntime ;; esac \
 && curl -sSLf --retry 5 -o /tmp/ort.nupkg "https://api.nuget.org/v3-flatcontainer/$pkg/1.24.4/$pkg.1.24.4.nupkg" \
 && mkdir -p /opt/ort/lib && unzip -j -q /tmp/ort.nupkg 'runtimes/linux-x64/native/*' -d /opt/ort/lib && rm /tmp/ort.nupkg \
 && apt-get purge -y curl unzip && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*
COPY --from=models /models /app/models
COPY --from=server /mediasorter-web /usr/local/bin/mediasorter-web
COPY protected.txt /app/protected.txt
ENV PS_DB=/data/faces.db HOME=/data/home PS_SCAN_SRC=/photos MS_MODELS=/app/models \
    MS_ORT=/opt/ort/lib/libonnxruntime.so MS_WEB_ADDR=:8501 MS_PROTECTED=/app/protected.txt
EXPOSE 8501
CMD ["mediasorter-web"]
