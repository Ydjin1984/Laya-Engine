#!/usr/bin/env bash
# ===========================================================================
# build.sh -- gofmt + vet + test + build bin/laya-server (Linux/macOS).
#
# Requirements: Go >= 1.26 and a C compiler (cgo is required by the ONNX
# Runtime binding). The ONNX Runtime shared library is NOT needed to compile
# (it is loaded at runtime), but it must be present to run.
#
# Usage: ./scripts/build.sh [--fast]     # --fast: skip vet/test
# ===========================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

FAST=0
[[ "${1:-}" == "--fast" ]] && FAST=1

die() { echo "[BUILD] ERROR: $*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || die "go is not in PATH. Install Go >= 1.26: https://go.dev/dl/"
if ! command -v cc >/dev/null 2>&1 && ! command -v gcc >/dev/null 2>&1 && ! command -v clang >/dev/null 2>&1; then
  die "no C compiler found (cgo needs one):
       Debian/Ubuntu: sudo apt-get install -y build-essential
       Fedora:        sudo dnf groupinstall -y 'Development Tools'
       Arch:          sudo pacman -S base-devel
       macOS:         xcode-select --install"
fi

export CGO_ENABLED=1

if [[ $FAST -eq 0 ]]; then
  echo "[BUILD] gofmt"
  UNFORMATTED="$(gofmt -l . || true)"
  [[ -z "$UNFORMATTED" ]] || { echo "$UNFORMATTED"; die "gofmt found unformatted files (run: gofmt -w .)"; }

  echo "[BUILD] go vet"
  go vet ./...

  echo "[BUILD] go test"
  go test ./... -timeout 10m
fi

echo "[BUILD] building bin/laya-server"
mkdir -p bin
go build -o bin/laya-server ./cmd/laya-server

echo "[BUILD] done: bin/laya-server ($(du -h bin/laya-server | cut -f1))"
echo "[BUILD] next: ./scripts/laya.sh start   (or ./scripts/install.sh for the full setup)"
