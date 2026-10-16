#!/usr/bin/env bash
# ===========================================================================
# laya.sh -- Laya Engine service control for Linux and macOS.
#
# Usage:
#   ./scripts/laya.sh start [--cpu] [--port N] [--device-id N]
#   ./scripts/laya.sh stop
#   ./scripts/laya.sh restart [...]
#   ./scripts/laya.sh status            # /ready, /info, pid, log tail
#   ./scripts/laya.sh logs              # tail -f the log
#   ./scripts/laya.sh selftest [--cpu]  # one question, then exit
#   ./scripts/laya.sh run    [...]      # foreground (for systemd/launchd/docker)
#
# Defaults: device=cuda (falls back to an explicit error if CUDA is not
# available -- the engine never silently degrades to CPU), port 8095,
# listener 127.0.0.1 only.
#
# Files:   models/multilingual-fp16.onnx, models/tokenizer.json
#          bin/laya-server, bin/libonnxruntime.so|.dylib
#          third_party/cuda12/  (CUDA 12 + cuDNN 9 shared libraries)
# Output:  logs/laya-server.log, logs/laya-server.pid
# ===========================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

LOG_DIR="$ROOT/logs"
LOG="$LOG_DIR/laya-server.log"
PIDFILE="$LOG_DIR/laya-server.pid"

DEVICE="${LAYA_DEVICE:-cuda}"
DEVICE_ID="${LAYA_DEVICE_ID:-0}"
PORT="${LAYA_PORT:-8095}"
WARMUP="${LAYA_WARMUP:-true}"
READY_TRIES="${LAYA_READY_TRIES:-120}"   # x0.5s = 60 s budget: first CUDA load is slow

# --- platform -----------------------------------------------------------------
UNAME_S="$(uname -s)"
case "$UNAME_S" in
  Linux)  PATH_SEP=":"; ORT_NAMES=("libonnxruntime.so");;
  Darwin) PATH_SEP=":"; ORT_NAMES=("libonnxruntime.dylib" "libonnxruntime.so");;
  *)      PATH_SEP=":"; ORT_NAMES=("libonnxruntime.so" "libonnxruntime.dylib");;
esac

# --- helpers ------------------------------------------------------------------
die() { echo "[LAYA] ERROR: $*" >&2; exit 1; }
info() { echo "[LAYA] $*"; }

find_ort() {
  # 1) explicit env, 2) bin/ next to the executable, 3) unpacked third_party tree
  if [[ -n "${LAYA_ORT:-}" && -f "${LAYA_ORT}" ]]; then echo "$LAYA_ORT"; return; fi
  local n
  for n in "${ORT_NAMES[@]}"; do
    if [[ -f "$ROOT/bin/$n" ]]; then echo "$ROOT/bin/$n"; return; fi
  done
  local cand
  for n in "${ORT_NAMES[@]}"; do
    cand="$(find "$ROOT/third_party" -maxdepth 4 -name "$n" -type f 2>/dev/null | head -1 || true)"
    if [[ -n "$cand" ]]; then echo "$cand"; return; fi
  done
  echo ""
}

find_binary() {
  if [[ -x "$ROOT/bin/laya-server" ]]; then echo "$ROOT/bin/laya-server"; return; fi
  if [[ -x "$ROOT/bin/laya-server.exe" ]]; then echo "$ROOT/bin/laya-server.exe"; return; fi
  echo ""
}

# CUDA library dirs: third_party/cuda12 and any nvidia-* pip layout the user has.
cuda_dirs() {
  local dirs=""
  [[ -d "$ROOT/third_party/cuda12" ]] && dirs="$ROOT/third_party/cuda12"
  if [[ -n "${LAYA_CUDA_DLL_DIRS:-}" ]]; then
    dirs="${dirs:+$dirs$PATH_SEP}$LAYA_CUDA_DLL_DIRS"
  fi
  echo "$dirs"
}

prepare_env() {
  local ort; ort="$(find_ort)"
  [[ -n "$ort" ]] || die "onnxruntime shared library not found in bin/ or third_party/.
         Run scripts/fetch-ort.sh (see docs/INSTALL.md)."
  local ort_dir; ort_dir="$(dirname "$ort")"

  local cuda; cuda="$(cuda_dirs)"
  local libdirs="$ort_dir"
  [[ -n "$cuda" ]] && libdirs="$libdirs$PATH_SEP$cuda"

  # The dynamic loader reads these at process start -- that is why they are set
  # here and not inside the engine.
  export LD_LIBRARY_PATH="$libdirs${LD_LIBRARY_PATH:+$PATH_SEP$LD_LIBRARY_PATH}"
  export DYLD_LIBRARY_PATH="$libdirs${DYLD_LIBRARY_PATH:+$PATH_SEP$DYLD_LIBRARY_PATH}"
  export LAYA_ORT="$ort"
  [[ -n "$cuda" ]] && export LAYA_CUDA_DLL_DIRS="$cuda"
}

check_files() {
  [[ -f "$ROOT/models/multilingual-fp16.onnx" ]] || die "models/multilingual-fp16.onnx is missing.
         Run scripts/fetch-model.sh (see docs/INSTALL.md)."
  [[ -f "$ROOT/models/tokenizer.json" ]] || die "models/tokenizer.json is missing.
         Run scripts/fetch-model.sh (see docs/INSTALL.md)."
}

server_args() {
  local cpu="$1" dev_id="$2" port="$3" warm="$4"
  local dev="cuda"; [[ "$cpu" == "1" ]] && dev="cpu"
  printf '%s\n' \
    "-addr" "127.0.0.1:$port" \
    "-device" "$dev" \
    "-device-id" "$dev_id" \
    "-model" "$ROOT/models/multilingual-fp16.onnx" \
    "-tokenizer" "$ROOT/models/tokenizer.json" \
    "-ort" "$LAYA_ORT" \
    "-warmup" "$warm" \
    "-log" "$LOG"
}

wait_ready() {
  local port="$1" i=0
  info "waiting for readiness on 127.0.0.1:$port ..."
  while (( i < READY_TRIES )); do
    if curl -fsS "http://127.0.0.1:$port/ready" >/dev/null 2>&1; then
      info "ready."
      return 0
    fi
    sleep 0.5; i=$((i+1))
  done
  echo "[LAYA] not ready after $((READY_TRIES/2)) s -- last log lines:" >&2
  tail -n 20 "$LOG" >&2 || true
  return 1
}

# --- actions ------------------------------------------------------------------
cmd_start() {
  local cpu=0 dev_id="$DEVICE_ID" port="$PORT" warm="$WARMUP"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --cpu) cpu=1;;
      --port) port="$2"; shift;;
      --device-id) dev_id="$2"; shift;;
      --no-warmup) warm="false";;
      *) die "unknown argument: $1";;
    esac
    shift
  done

  check_files
  prepare_env
  local bin; bin="$(find_binary)"
  [[ -n "$bin" ]] || die "bin/laya-server not found. Build it (scripts/build.sh) or
         download the release asset (docs/INSTALL.md)."

  if [[ -f "$PIDFILE" ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    die "already running (pid $(cat "$PIDFILE")) -- use 'restart' or 'stop'."
  fi

  mkdir -p "$LOG_DIR"
  info "starting: $bin (device=$([[ $cpu == 1 ]] && echo cpu || echo cuda), port=$port)"
  # shellcheck disable=SC2046
  nohup "$bin" $(server_args "$cpu" "$dev_id" "$port" "$warm") >>"$LOG" 2>&1 &
  echo $! >"$PIDFILE"
  info "pid $(cat "$PIDFILE"), log: $LOG"

  if ! wait_ready "$port"; then
    info "stopping the failed process"
    kill "$(cat "$PIDFILE")" 2>/dev/null || true
    rm -f "$PIDFILE"
    exit 1
  fi
  curl -fsS "http://127.0.0.1:$port/info" 2>/dev/null | head -c 600; echo
}

cmd_run() {
  # Foreground mode for systemd/launchd/docker: no daemonizing, no pidfile.
  local cpu=0 dev_id="$DEVICE_ID" port="$PORT" warm="$WARMUP"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --cpu) cpu=1;;
      --port) port="$2"; shift;;
      --device-id) dev_id="$2"; shift;;
      --no-warmup) warm="false";;
      *) die "unknown argument: $1";;
    esac
    shift
  done
  check_files
  prepare_env
  local bin; bin="$(find_binary)"
  [[ -n "$bin" ]] || die "bin/laya-server not found -- run scripts/build.sh first."
  mkdir -p "$LOG_DIR"
  # shellcheck disable=SC2046
  exec "$bin" $(server_args "$cpu" "$dev_id" "$port" "$warm")
}

cmd_stop() {
  if [[ ! -f "$PIDFILE" ]]; then
    info "no pidfile -- nothing to stop."
    return 0
  fi
  local pid; pid="$(cat "$PIDFILE")"
  if ! kill -0 "$pid" 2>/dev/null; then
    info "process $pid is gone; removing stale pidfile."
    rm -f "$PIDFILE"
    return 0
  fi
  # Refuse to kill anything that is not laya-server.
  local comm; comm="$(ps -p "$pid" -o comm= 2>/dev/null || true)"
  if [[ "$comm" != *laya-server* ]]; then
    die "pid $pid is '$comm', not laya-server -- refusing to stop it. pidfile: $PIDFILE"
  fi
  info "stopping pid $pid ..."
  kill "$pid" 2>/dev/null || true
  local i=0
  while kill -0 "$pid" 2>/dev/null && (( i < 20 )); do sleep 0.5; i=$((i+1)); done
  if kill -0 "$pid" 2>/dev/null; then
    info "did not exit in 10 s -- sending SIGKILL"
    kill -9 "$pid" 2>/dev/null || true
  fi
  rm -f "$PIDFILE"
  info "stopped."
}

cmd_status() {
  local port="${1:-$PORT}"
  if [[ -f "$PIDFILE" ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    info "process: running (pid $(cat "$PIDFILE"))"
  else
    info "process: not running (or no pidfile)"
  fi
  if curl -fsS "http://127.0.0.1:$port/ready" >/dev/null 2>&1; then
    info "ready:   yes"
    curl -fsS "http://127.0.0.1:$port/info" | sed 's/^/  /' | head -40
  else
    info "ready:   no"
    [[ -f "$LOG" ]] && { echo "  --- last log lines ---"; tail -n 15 "$LOG" | sed 's/^/  /'; }
  fi
}

cmd_logs() { [[ -f "$LOG" ]] || die "no log yet: $LOG"; tail -n 100 -f "$LOG"; }

cmd_selftest() {
  local cpu=0
  [[ "${1:-}" == "--cpu" ]] && cpu=1
  check_files
  prepare_env
  local bin; bin="$(find_binary)"
  [[ -n "$bin" ]] || die "bin/laya-server not found -- run scripts/build.sh first."
  # shellcheck disable=SC2046
  "$bin" $(server_args "$cpu" "$DEVICE_ID" "$PORT" "false") -selftest
}

# --- dispatch -----------------------------------------------------------------
ACTION="${1:-}"; shift || true
case "$ACTION" in
  start)    cmd_start "$@";;
  run)      cmd_run "$@";;
  stop|kill) cmd_stop ;;
  restart)  cmd_stop; cmd_start "$@";;
  status)   cmd_status "$@";;
  logs)     cmd_logs ;;
  selftest) cmd_selftest "$@";;
  ""|-h|--help|help)
    sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    ;;
  *) die "unknown action '$ACTION' (start|stop|restart|status|logs|selftest|run)";;
esac
