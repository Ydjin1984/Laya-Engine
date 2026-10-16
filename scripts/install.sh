#!/usr/bin/env bash
# ===========================================================================
# install.sh -- one-shot installer for Laya Engine on Linux and macOS.
#
# What it does, in order:
#   1. checks prerequisites (curl; Go + C compiler only if building from source)
#   2. downloads the weights + tokenizer (scripts/fetch-model.sh, SHA-256 checked)
#   3. downloads ONNX Runtime 1.25.0 for this platform (scripts/fetch-ort.sh)
#   4. optionally downloads CUDA 12 / cuDNN 9 libraries (scripts/fetch-cuda.sh)
#   5. builds the engine (or downloads the prebuilt release binary)
#   6. optionally installs the service (systemd user unit / launchd agent)
#   7. optionally installs the ZCode plugin + skill (scripts/install-plugin.sh)
#   8. runs a self-test and prints the exact next commands
#
# Usage:
#   ./scripts/install.sh                 # GPU if available, build from source
#   ./scripts/install.sh --cpu           # CPU-only install (no CUDA downloads)
#   ./scripts/install.sh --prebuilt      # no Go toolchain needed
#   ./scripts/install.sh --service       # + autostart on login (systemd/launchd)
#   ./scripts/install.sh --plugin        # + ZCode plugin and skill
#   ./scripts/install.sh --port 8095     # listen port (default 8095)
# ===========================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

CPU=0
PREBUILT=0
SERVICE=0
PLUGIN=0
PORT="${LAYA_PORT:-8095}"
FORCE_GPU=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cpu)      CPU=1;;
    --gpu)      FORCE_GPU=1;;
    --prebuilt) PREBUILT=1;;
    --service)  SERVICE=1;;
    --plugin)   PLUGIN=1;;
    --port)     PORT="$2"; shift;;
    -h|--help)  sed -n '2,24p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0;;
    *) echo "[INSTALL] unknown argument: $1" >&2; exit 1;;
  esac
  shift
done

info() { echo; echo "=== [INSTALL] $* ==="; }
die()  { echo "[INSTALL] ERROR: $*" >&2; exit 1; }

UNAME_S="$(uname -s)"
REL_NAME="laya-server-linux-amd64"
case "$UNAME_S" in
  Linux)  [[ "$(uname -m)" == "aarch64" ]] && REL_NAME="laya-server-linux-arm64";;
  Darwin) REL_NAME="laya-server-darwin-amd64"; [[ "$(uname -m)" == "arm64" ]] && REL_NAME="laya-server-darwin-arm64";;
  *) die "unsupported OS '$UNAME_S'; on Windows run: scripts\\windows\\install.ps1";;
esac

# --- 1. prerequisites ---------------------------------------------------------
info "prerequisites"
command -v curl >/dev/null 2>&1 || die "curl is required."
HAVE_GO=0; command -v go >/dev/null 2>&1 && HAVE_GO=1
if [[ $PREBUILT -eq 0 && $HAVE_GO -eq 0 ]]; then
  echo "[INSTALL] Go is not installed. Two options:"
  echo "          a) install Go >= 1.26 (https://go.dev/dl/) and re-run, or"
  echo "          b) re-run with --prebuilt to download the release binary."
  die "no Go toolchain."
fi

# --- 2. weights ---------------------------------------------------------------
if [[ -f models/multilingual-fp16.onnx && -f models/tokenizer.json ]]; then
  info "weights already present -- skipping download"
else
  info "downloading weights and tokenizer (650 MB, SHA-256 verified)"
  ./scripts/fetch-model.sh
fi

# --- 3. ONNX Runtime ----------------------------------------------------------
if ls bin/libonnxruntime.so* bin/libonnxruntime*.dylib >/dev/null 2>&1; then
  info "ONNX Runtime already present -- skipping download"
else
  if [[ $CPU -eq 1 ]]; then
    info "downloading ONNX Runtime (CPU build)"
    ./scripts/fetch-ort.sh --cpu
  else
    info "downloading ONNX Runtime (GPU build on Linux, CPU elsewhere)"
    ./scripts/fetch-ort.sh
  fi
fi

# --- 4. CUDA ------------------------------------------------------------------
if [[ $CPU -eq 0 && "$UNAME_S" == "Linux" ]]; then
  if [[ -f third_party/cuda12/libcudnn.so.9 || -f third_party/cuda12/libcublas.so.12 ]]; then
    info "CUDA 12 libraries already present -- skipping download"
  elif [[ $FORCE_GPU -eq 1 ]] || command -v nvidia-smi >/dev/null 2>&1; then
    info "downloading CUDA 12 / cuDNN 9 libraries (~1.5 GB from PyPI wheels)"
    ./scripts/fetch-cuda.sh
  else
    echo "[INSTALL] no NVIDIA GPU detected (nvidia-smi missing) -- CPU mode."
    CPU=1
  fi
fi
if [[ $CPU -eq 1 ]]; then
  echo "[INSTALL] CPU mode: the service will run with -device cpu (slow, but needs no NVIDIA stack)."
fi

# --- 5. binary ----------------------------------------------------------------
if [[ $PREBUILT -eq 1 ]]; then
  info "downloading prebuilt binary ($REL_NAME)"
  mkdir -p bin
  URL="https://github.com/Ydjin1984/Laya-Engine/releases/latest/download/$REL_NAME"
  curl -fL --retry 3 -o bin/laya-server "$URL" || die "download failed: $URL
       Build from source instead: remove --prebuilt (needs Go + a C compiler)."
  chmod +x bin/laya-server
else
  info "building from source"
  ./scripts/build.sh
fi

# --- 6. service ---------------------------------------------------------------
if [[ $SERVICE -eq 1 ]]; then
  info "installing the service (autostart)"
  if [[ "$UNAME_S" == "Linux" ]]; then
    UNIT_DIR="$HOME/.config/systemd/user"
    mkdir -p "$UNIT_DIR"
    sed -e "s|__ROOT__|$ROOT|g" -e "s|__PORT__|$PORT|g" \
        -e "s|__CPUFLAG__|$([[ $CPU -eq 1 ]] && echo '--cpu' || echo '')|" \
        scripts/service/laya-engine.service > "$UNIT_DIR/laya-engine.service"
    systemctl --user daemon-reload
    systemctl --user enable --now laya-engine.service
    echo "[INSTALL] service enabled: systemctl --user status laya-engine"
    echo "[INSTALL] to keep it running without an active login: loginctl enable-linger $USER"
  else
    PLIST_DIR="$HOME/Library/LaunchAgents"
    mkdir -p "$PLIST_DIR"
    sed -e "s|__ROOT__|$ROOT|g" -e "s|__PORT__|$PORT|g" -e "s|__DEVICE__|$([[ $CPU -eq 1 ]] && echo cpu || echo cuda)|" \
        scripts/service/com.davinci.laya-engine.plist > "$PLIST_DIR/com.davinci.laya-engine.plist"
    launchctl unload "$PLIST_DIR/com.davinci.laya-engine.plist" 2>/dev/null || true
    launchctl load "$PLIST_DIR/com.davinci.laya-engine.plist"
    echo "[INSTALL] launchd agent installed: launchctl list | grep davinci"
  fi
fi

# --- 7. ZCode plugin + skill --------------------------------------------------
if [[ $PLUGIN -eq 1 ]]; then
  info "installing the ZCode plugin and skill"
  ./scripts/install-plugin.sh || echo "[INSTALL] plugin step failed -- see the messages above; the engine itself is installed."
fi

# --- 8. verify ----------------------------------------------------------------
info "self-test"
if [[ $SERVICE -eq 1 ]]; then
  # The service owns the port; just check readiness.
  for _ in $(seq 1 120); do curl -fsS "http://127.0.0.1:$PORT/ready" >/dev/null 2>&1 && break; sleep 0.5; done
  ./scripts/laya.sh status "$PORT" || true
else
  if [[ $CPU -eq 1 ]]; then ./scripts/laya.sh selftest --cpu || true; else ./scripts/laya.sh selftest || true; fi
fi

cat <<EOF

================= Laya Engine installed =================
  start:   ./scripts/laya.sh start $([[ $CPU -eq 1 ]] && echo --cpu)
  status:  ./scripts/laya.sh status
  stop:    ./scripts/laya.sh stop
  logs:    ./scripts/laya.sh logs
  API:     http://127.0.0.1:$PORT/info
  plugin:  $([[ $PLUGIN -eq 1 ]] && echo 'installed -- enable it in ZCode: Settings -> Plugin Management' || echo './scripts/install-plugin.sh  (then enable it in ZCode)')
  docs:    docs/INSTALL.md, docs/API.md
=========================================================
EOF
