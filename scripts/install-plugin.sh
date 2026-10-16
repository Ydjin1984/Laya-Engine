#!/usr/bin/env bash
# ===========================================================================
# install-plugin.sh -- install the ZCode plugin + skill "davinci-laya-decisions"
# from the public GitHub marketplace, for Linux and macOS.
#
# What it does:
#   1. checks Node.js >= 20 (required by the plugin)
#   2. clones (or downloads) https://github.com/Ydjin1984/davinci-laya-zcode
#   3. copies the plugin into the local ZCode plugin workspace
#      (~/.zcode/plugin-workspace/plugins/davinci-laya-decisions)
#   4. registers it in the workspace marketplace.json (backup kept)
#   5. on non-Windows hosts, builds the hook launcher (bin/laya-hook.exe) --
#      the plugin's hooks point at that path; without it, hooks cost one extra
#      Node start per event. If Go is unavailable, a pass-through shell wrapper
#      is installed instead (same behaviour, slightly slower).
#
# The plugin and its skill are enabled in ZCode's UI afterwards:
#   Settings -> Plugin Management -> enable "DaVinci x Laya: local decisions",
#   then start a new session and check /laya-status.
#
# Usage: ./scripts/install-plugin.sh [--no-launcher]
# ===========================================================================
set -euo pipefail

REPO="https://github.com/Ydjin1984/davinci-laya-zcode.git"
PLUGIN_DIR_NAME="davinci-laya-decisions"
WORKSPACE="${ZCODE_PLUGIN_WORKSPACE:-$HOME/.zcode/plugin-workspace}"
DEST="$WORKSPACE/plugins/$PLUGIN_DIR_NAME"
MARKETPLACE="$WORKSPACE/plugins/marketplace.json"
NO_LAUNCHER=0
[[ "${1:-}" == "--no-launcher" ]] && NO_LAUNCHER=1

info() { echo "[PLUGIN] $*"; }
die()  { echo "[PLUGIN] ERROR: $*" >&2; exit 1; }

# --- 1. node ------------------------------------------------------------------
if ! command -v node >/dev/null 2>&1; then
  die "Node.js is not installed -- the plugin requires Node >= 20.
       Ubuntu/Debian:  sudo apt-get install -y nodejs npm   (or use nodesource/nvm for a newer version)
       Fedora:         sudo dnf install -y nodejs
       Arch:           sudo pacman -S nodejs npm
       macOS:          brew install node"
fi
NODE_MAJOR="$(node -p 'process.versions.node.split(".")[0]')"
[[ "$NODE_MAJOR" -ge 20 ]] || die "Node $NODE_MAJOR is too old -- the plugin requires >= 20."

# --- 2. fetch the repo --------------------------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if command -v git >/dev/null 2>&1; then
  info "cloning $REPO ..."
  git clone --depth 1 -q "$REPO" "$TMP/davinci-laya-zcode"
else
  info "git not found -- downloading the repository archive ..."
  mkdir -p "$TMP/davinci-laya-zcode"
  curl -fL --retry 3 -o "$TMP/repo.tar.gz" \
    "https://codeload.github.com/Ydjin1984/davinci-laya-zcode/tar.gz/refs/heads/main" \
    || die "download failed (is the repository reachable?)"
  tar -xzf "$TMP/repo.tar.gz" -C "$TMP/davinci-laya-zcode" --strip-components=1
fi

SRC="$TMP/davinci-laya-zcode/plugins/$PLUGIN_DIR_NAME"
[[ -d "$SRC" ]] || die "unexpected repository layout: $SRC not found"

# --- 3. copy ------------------------------------------------------------------
info "installing plugin into $DEST ..."
mkdir -p "$WORKSPACE/plugins"
rm -rf "$DEST"
cp -a "$SRC" "$DEST"

# --- 4. hook launcher ---------------------------------------------------------
if [[ $NO_LAUNCHER -eq 0 ]]; then
  LAUNCHER="$DEST/bin/laya-hook.exe"          # name fixed by hooks/hooks.json
  ERRLOG="$DEST/bin/laya-hook.err.log"
  if [[ -f "$LAUNCHER" ]] && file "$LAUNCHER" 2>/dev/null | grep -qi "PE32\|MS-DOS"; then
    info "a Windows launcher binary was copied; replacing it with a native one ..."
    rm -f "$LAUNCHER"
  fi
  if [[ ! -f "$LAUNCHER" ]]; then
    mkdir -p "$DEST/bin"
    if command -v go >/dev/null 2>&1 && (cd "$DEST" && go build -o "$LAUNCHER" ./tools/hook-launcher 2>"$ERRLOG"); then
      info "hook launcher built with Go (fast path for read-only commands)."
      rm -f "$ERRLOG"
    else
      info "Go is unavailable -- installing a pass-through launcher instead."
      info "(everything works; hooks just spawn Node directly, ~0.2 s per event)"
      cat > "$LAUNCHER" <<'SH'
#!/usr/bin/env bash
# Pass-through replacement for the Go hook launcher (hooks/hooks.json calls
# this path as: laya-hook.exe <pre|post> <hook script> ...). It skips the
# read-only fast path and always runs the Node hook -- same behaviour, slower.
exec node "$2" "${@:3}"
SH
      chmod +x "$LAUNCHER"
    fi
  fi
  chmod +x "$LAUNCHER" 2>/dev/null || true
fi

# --- 5. marketplace.json ------------------------------------------------------
info "registering the plugin in the workspace marketplace ..."
# The entry (names/descriptions) is taken from the repository's own
# marketplace.json, so this script does not duplicate (and cannot drift from)
# the canonical metadata.
node - "$MARKETPLACE" "$TMP/davinci-laya-zcode/marketplace.json" <<'JS'
const fs = require("fs");
const [mpPath, srcPath] = process.argv.slice(2);
const read = (p) => JSON.parse(fs.readFileSync(p, "utf8").replace(/^\uFEFF/, ""));
let mp;
try { mp = read(mpPath); }
catch { mp = { name: "dev-plugin-workspace", plugins: [] }; }
if (!Array.isArray(mp.plugins)) mp.plugins = [];
fs.writeFileSync(mpPath + ".bak", JSON.stringify(mp, null, 2) + "\n");
let entry = null;
try {
  const src = read(srcPath);
  entry = (src.plugins || []).find((p) => p && p.name === "davinci-laya-decisions") || null;
} catch {}
if (!entry) entry = { name: "davinci-laya-decisions", version: "0.1.0" };
entry.source = "./davinci-laya-decisions";
const i = mp.plugins.findIndex((p) => p && p.name === entry.name);
if (i >= 0) mp.plugins[i] = Object.assign({}, mp.plugins[i], entry);
else mp.plugins.push(entry);
fs.writeFileSync(mpPath, JSON.stringify(mp, null, 2) + "\n");
console.log("[PLUGIN] marketplace updated:", mpPath, "(backup: " + mpPath + ".bak)");
JS

# --- 6. next steps ------------------------------------------------------------
cat <<EOF

================= ZCode plugin installed =================
  The plugin files are in: $DEST
  Now, in ZCode:
    1. Settings -> Plugin Management -> refresh the marketplace list
       (marketplace: dev-plugin-workspace), enable
       "DaVinci x Laya: local decisions".
    2. Start a NEW session (hooks, commands, skill and MCP tools load at start).
    3. Approve the MCP server when asked, then check: /laya-status
  Alternative (canonical) install path, works without this script:
    Settings -> Plugin Management -> Discover -> "+" -> GitHub repository ->
    Ydjin1984/davinci-laya-zcode  (public), then enable the plugin.
  The engine must be running on 127.0.0.1:8095 for active features:
    ./scripts/laya.sh start
  Without the engine the plugin stays in a passive, fail-open mode.
=========================================================
EOF
