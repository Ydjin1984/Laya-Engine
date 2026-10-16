#!/usr/bin/env bash
# ===========================================================================
# fetch-ort.sh -- download ONNX Runtime 1.25.0 for this platform and unpack it
# into third_party/ + bin/.
#
# Why exactly 1.25.0: the engine is built against the ONNX Runtime C API that
# this release provides, and the checkpoint was calibrated on it. Newer
# runtimes are not a drop-in replacement (see docs/ENGINE.ru.md).
#
# Usage:
#   ./scripts/fetch-ort.sh          # GPU build on Linux, CPU build elsewhere
#   ./scripts/fetch-ort.sh --cpu    # CPU-only build (works everywhere)
#   ./scripts/fetch-ort.sh --gpu    # CUDA execution provider (Linux only)
#   ORT_VERSION=1.25.0 ./scripts/fetch-ort.sh   # override the version
# ===========================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

ORT_VERSION="${ORT_VERSION:-1.25.0}"
MODE="${1:-auto}"

UNAME_S="$(uname -s)"
UNAME_M="$(uname -m)"

case "$UNAME_S" in
  Linux)  OS="linux";;
  Darwin) OS="osx";;
  *)      echo "[ORT] ERROR: unsupported OS '$UNAME_S' (Windows: use scripts/windows/fetch-ort.ps1)" >&2; exit 1;;
esac

case "$UNAME_M" in
  x86_64|amd64) ARCH="x64";;
  aarch64|arm64) ARCH="arm64";;
  *) echo "[ORT] ERROR: unsupported architecture '$UNAME_M'" >&2; exit 1;;
esac

if [[ "$MODE" == "auto" ]]; then
  if [[ "$OS" == "linux" ]]; then MODE="gpu"; else MODE="cpu"; fi
fi

# CUDA execution provider ships for linux-x64 only; macOS and arm64 run on CPU.
FLAVOR=""
if [[ "$MODE" == "--gpu" ]]; then
  if [[ "$OS" != "linux" || "$ARCH" != "x64" ]]; then
    echo "[ORT] NOTE: GPU builds exist for linux-x64 only; continuing with the CPU build."
  else
    FLAVOR="-gpu"
  fi
fi

PKG="onnxruntime-${OS}-${ARCH}${FLAVOR}-${ORT_VERSION}"
EXT="tgz"
URL="https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/${PKG}.${EXT}"

echo "[ORT] package : $PKG"
echo "[ORT] url     : $URL"

DEST="$ROOT/third_party/$PKG"
if [[ ! -d "$DEST" ]]; then
  mkdir -p "$ROOT/third_party"
  TMP="$ROOT/third_party/${PKG}.tgz.part"
  echo "[ORT] downloading ..."
  if ! curl -fL --retry 3 --retry-delay 2 -o "$TMP" "$URL"; then
    rm -f "$TMP"
    echo "[ORT] ERROR: download failed. Check that the package exists for your platform:" >&2
    echo "       https://github.com/microsoft/onnxruntime/releases/tag/v${ORT_VERSION}" >&2
    exit 1
  fi
  mv "$TMP" "$ROOT/third_party/${PKG}.tgz"
  tar -xzf "$ROOT/third_party/${PKG}.tgz" -C "$ROOT/third_party"
  rm -f "$ROOT/third_party/${PKG}.tgz"
else
  echo "[ORT] already unpacked at third_party/$PKG"
fi

# Copy the shared libraries next to the engine binary. The engine loads
# libonnxruntime by path (see -ort / LAYA_ORT); keeping a copy in bin/ makes
# the default layout self-contained. Symlinks are preserved (-a).
mkdir -p "$ROOT/bin"
cp -a "$DEST"/lib/libonnxruntime.so* "$ROOT/bin/" 2>/dev/null || true
cp -a "$DEST"/lib/libonnxruntime*.dylib "$ROOT/bin/" 2>/dev/null || true
if [[ -n "$FLAVOR" ]]; then
  cp -a "$DEST"/lib/libonnxruntime_providers_cuda.so "$ROOT/bin/" 2>/dev/null || true
  cp -a "$DEST"/lib/libonnxruntime_providers_shared.so "$ROOT/bin/" 2>/dev/null || true
fi

echo "[ORT] done:"
ls -la "$ROOT/bin" | grep -i onnxruntime || true
if [[ -n "$FLAVOR" ]]; then
  echo "[ORT] CUDA execution provider included. Next: ./scripts/fetch-cuda.sh"
fi
echo "[ORT] Next: ./scripts/build.sh (if you build from source) or scripts/install.sh."
