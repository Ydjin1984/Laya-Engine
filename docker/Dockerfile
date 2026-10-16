# syntax=docker/dockerfile:1
# ===========================================================================
# Laya Engine -- CPU image (works on any Docker host, no NVIDIA stack).
#
# Build context: the repository root (docker compose handles this).
#   docker build -f docker/Dockerfile -t laya-engine:cpu .
#   docker run --rm -p 127.0.0.1:8095:8095 -v "$PWD/models:/app/models:ro" laya-engine:cpu
#
# Weights are NOT baked into the image: they are mounted from ./models
# (fetch them once with scripts/fetch-model.sh). This keeps the image small and
# makes the checkpoint explicit.
# ===========================================================================
FROM golang:1.26-bookworm AS builder

ARG ORT_VERSION=1.25.0

RUN apt-get update \
 && apt-get install -y --no-install-recommends curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# ONNX Runtime 1.25.0 (CPU build) -- the C API the engine is built against.
RUN curl -fL --retry 3 -o /tmp/ort.tgz \
      "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-linux-x64-${ORT_VERSION}.tgz" \
 && tar -xzf /tmp/ort.tgz -C /tmp \
 && mv "/tmp/onnxruntime-linux-x64-${ORT_VERSION}" /opt/ort \
 && rm /tmp/ort.tgz

ENV CGO_ENABLED=1
RUN go build -o /out/laya-server ./cmd/laya-server

# ---------------------------------------------------------------------------
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
RUN mkdir -p /app/logs

COPY --from=builder /opt/ort/lib/libonnxruntime.so* /app/lib/
COPY --from=builder /out/laya-server /app/bin/laya-server
COPY models/model-manifest.json /app/models/model-manifest.json

# Inside the container the service must listen on all interfaces (otherwise the
# port cannot be published); bind the published port to 127.0.0.1 on the host.
ENV LAYA_ADDR=0.0.0.0:8095 \
    LAYA_MODEL=/app/models/multilingual-fp16.onnx \
    LAYA_TOKENIZER=/app/models/tokenizer.json \
    LAYA_ORT=/app/lib/libonnxruntime.so \
    LAYA_DEVICE=cpu \
    LAYA_WARMUP=false \
    LAYA_LOG=/app/logs/laya-server.log

EXPOSE 8095

# First start loads ~700 MB of weights: give it time before healthchecks bite.
HEALTHCHECK --interval=30s --timeout=3s --start-period=150s --retries=5 \
  CMD curl -fsS http://127.0.0.1:8095/health || exit 1

ENTRYPOINT ["/app/bin/laya-server"]
CMD ["-addr", "0.0.0.0:8095", "-device", "cpu"]
