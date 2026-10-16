#!/usr/bin/env bash
# ===========================================================================
# fetch-model.sh -- one-time download of the Laya weights and tokenizer.
#
# The service itself NEVER downloads anything: this script is the only thing
# that touches the network, and only during installation. Both files are
# verified against SHA-256 from models/model-manifest.json; a mismatch is a
# hard error (a silently different checkpoint would invalidate every threshold
# and measurement made with it).
#
# Usage:  ./scripts/fetch-model.sh [--force]
# Sources (public, no auth):
#   model:     https://huggingface.co/mizchi/laya-multilingual-onnx
#   tokenizer: https://huggingface.co/techtheist/laya-onnx
# ===========================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
FORCE="${1:-}"

MODEL_URL="https://huggingface.co/mizchi/laya-multilingual-onnx/resolve/main/model.onnx"
TOK_URL="https://huggingface.co/techtheist/laya-onnx/resolve/main/multilingual/tokenizer.json"

# Expected digests, taken from models/model-manifest.json (kept inline so the
# script works even without python/jq and is easy to audit).
MODEL_SHA="0b095e005a4c295cae74d47b7eb6931c369d48f5b720b45278d774165798310c"
TOK_SHA="609d8f4c067cd3950f88594c5a802616cea245823836ef5848ee4fc40aab5b6f"

die() { echo "[FETCH] ERROR: $*" >&2; exit 1; }
info() { echo "[FETCH] $*"; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1; then openssl dgst -sha256 "$1" | awk '{print $NF}'
  else die "no sha256sum/shasum/openssl available -- cannot verify the download."
  fi
}

fetch_one() {
  local url="$1" dest="$2" want="$3" name="$4"
  mkdir -p "$(dirname "$dest")"

  if [[ -f "$dest" && "$FORCE" != "--force" ]]; then
    local have; have="$(sha256_of "$dest")"
    if [[ "$have" == "$want" ]]; then
      info "$name already present and verified -- skipping (use --force to re-download)."
      return 0
    fi
    info "$name exists but its SHA-256 differs -- re-downloading."
  fi

  info "downloading $name ..."
  if ! curl -fL --retry 3 --retry-delay 2 -C - -o "$dest.part" "$url"; then
    rm -f "$dest.part"
    die "download failed: $url"
  fi
  mv "$dest.part" "$dest"

  info "verifying $name ..."
  local have; have="$(sha256_of "$dest")"
  if [[ "$have" != "$want" ]]; then
    rm -f "$dest"
    die "$name SHA-256 mismatch:
       expected $want
       got      $have
     The file was removed. Check your network/proxy, then retry."
  fi
  info "$name OK ($(du -h "$dest" | cut -f1))."
}

fetch_one "$MODEL_URL" "$ROOT/models/multilingual-fp16.onnx" "$MODEL_SHA" "multilingual-fp16.onnx (617 MB)"
fetch_one "$TOK_URL"   "$ROOT/models/tokenizer.json"          "$TOK_SHA"   "tokenizer.json (33 MB)"

# Cross-check against the manifest when python3 is available (defence in depth:
# if the manifest ever changes, this script must change with it).
if command -v python3 >/dev/null 2>&1 && [[ -f "$ROOT/models/model-manifest.json" ]]; then
  python3 - "$ROOT/models/model-manifest.json" <<'PY' || die "manifest cross-check failed"
import json, sys
m = json.load(open(sys.argv[1], encoding="utf-8"))["files"]
assert m["multilingual-fp16.onnx"]["sha256"] == "0b095e005a4c295cae74d47b7eb6931c369d48f5b720b45278d774165798310c", "manifest model sha256 changed"
assert m["tokenizer.json"]["sha256"] == "609d8f4c067cd3950f88594c5a802616cea245823836ef5848ee4fc40aab5b6f", "manifest tokenizer sha256 changed"
print("[FETCH] manifest cross-check OK")
PY
fi

info "done. Next: ./scripts/fetch-ort.sh  (and ./scripts/fetch-cuda.sh for GPU)."
