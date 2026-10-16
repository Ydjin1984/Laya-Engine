#!/usr/bin/env bash
# ===========================================================================
# fetch-cuda.sh -- CUDA 12 runtime libraries for the ONNX Runtime GPU build
# (Linux). Downloads the public NVIDIA wheels from PyPI and unpacks the shared
# libraries into third_party/cuda12/.
#
# Nothing is redistributed by this repository: the wheels are downloaded from
# PyPI, and their use is governed by the NVIDIA CUDA Toolkit EULA
# (https://docs.nvidia.com/cuda/eula/index.html). By downloading you accept it.
#
# Download size is about 1.5 GB; the wheels are the same artifacts that
# `pip install nvidia-cublas-cu12 ...` would install.
#
# Usage:  ./scripts/fetch-cuda.sh [--deps]   # --deps: also install unzip via the package manager hint
# ===========================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DEST="$ROOT/third_party/cuda12"
TMP="$ROOT/third_party/.cuda-wheel-tmp"
PKGS=(
  "nvidia-cuda-runtime-cu12"   # libcudart.so.12
  "nvidia-cublas-cu12"         # libcublas.so.12, libcublasLt.so.12
  "nvidia-cudnn-cu12"          # libcudnn.so.9 (+ sub-libraries)
  "nvidia-cufft-cu12"          # libcufft.so.11
  "nvidia-cuda-nvrtc-cu12"     # libnvrtc.so.12 (optional, used by some fusions)
)

info() { echo "[CUDA] $*"; }
die() { echo "[CUDA] ERROR: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required."
if ! command -v unzip >/dev/null 2>&1; then
  die "unzip is required. Install it first:
       Debian/Ubuntu: sudo apt-get install -y unzip
       Fedora:        sudo dnf install -y unzip
       Arch:          sudo pacman -S unzip
       openSUSE:      sudo zypper install unzip"
fi

mkdir -p "$DEST" "$TMP"

pick_wheel_url() {
  # Latest manylinux x86_64 wheel URL from the PyPI simple index (no python needed).
  local pkg="$1" arch="$2"
  curl -fsS "https://pypi.org/simple/${pkg}/" \
    | grep -oE 'https://files\.pythonhosted\.org[^"#]*\.whl' \
    | grep -E "manylinux[^/]*(x86_64|aarch64)" \
    | grep -E "$arch" \
    | tail -1
}

ARCH_RE="x86_64"
if [[ "$(uname -m)" == "aarch64" ]]; then ARCH_RE="aarch64"; fi

for pkg in "${PKGS[@]}"; do
  url="$(pick_wheel_url "$pkg" "$ARCH_RE" || true)"
  if [[ -z "$url" ]]; then
    if [[ "$pkg" == *nvrtc* ]]; then
      info "skip $pkg (no wheel for $ARCH_RE) -- optional."
      continue
    fi
    die "could not find a $ARCH_RE wheel for $pkg"
  fi
  whl="$TMP/$(basename "$url")"
  if [[ ! -f "$whl" ]]; then
    info "downloading $(basename "$url") ($(curl -sIL "$url" | grep -i '^content-length' | tail -1 | tr -d '\r' | awk '{printf "%.0f MB", $2/1048576}')) ..."
    curl -fL --retry 3 --retry-delay 2 -o "$whl" "$url" || die "download failed: $url"
  else
    info "cached: $(basename "$whl")"
  fi
  info "unpacking $(basename "$whl") ..."
  unzip -q -o "$whl" 'nvidia/*/lib/*' -d "$TMP/extract"
  # Also take the top-level lib dirs some wheels use.
  unzip -q -o "$whl" 'lib/*' -d "$TMP/extract" 2>/dev/null || true
done

info "collecting libraries into third_party/cuda12/ ..."
find "$TMP/extract" -type f -name '*.so*' -exec cp -a {} "$DEST/" \;
find "$TMP/extract" -type l -name '*.so*' -exec cp -a {} "$DEST/" \; 2>/dev/null || true

# Some wheels store only the fully versioned name; recreate the SONAME links
# the loader looks for (libcublas.so.12 -> libcublas.so.12.x.y.z).
shopt -s nullglob
for f in "$DEST"/*.so.*; do
  case "$f" in
    *.so.*.*.*)  # three-component version: build .so.N symlink
      short="$(echo "$f" | sed -E 's/^(.*\.so\.[0-9]+)\..*$/\1/')"
      [[ -e "$short" ]] || ln -sf "$(basename "$f")" "$short"
      ;;
  esac
done
shopt -u nullglob

rm -rf "$TMP"

info "done. Libraries in third_party/cuda12/:"
ls "$DEST" | grep -E 'libcudart|libcublas|libcudnn\.so|libcufft' || true
cat <<'EOF'
[CUDA] Next steps:
  ./scripts/laya.sh start          # start on the GPU (default device=cuda)
  ./scripts/laya.sh selftest       # verify the CUDA provider actually loaded
If the engine reports that the CUDA provider failed to load, check that the
names above include libcudart.so.12, libcublas.so.12, libcublasLt.so.12,
libcudnn.so.9 and libcufft.so.11.
EOF
