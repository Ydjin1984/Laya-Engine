# ===========================================================================
# install-plugin.ps1 -- install the ZCode plugin + skill "davinci-laya-decisions"
# from the public GitHub marketplace (Windows).
#
# Steps:
#   1. check Node.js >= 20 (required by the plugin)
#   2. clone (or download) https://github.com/Ydjin1984/davinci-laya-zcode
#   3. copy the plugin into %USERPROFILE%\.zcode\plugin-workspace\plugins\
#   4. register it in the workspace marketplace.json (backup kept)
#   5. print the exact ZCode UI steps to enable it
#
# The plugin ships its own Windows hook launcher (bin\laya-hook.exe), so
# nothing has to be built here.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\windows\install-plugin.ps1
# ===========================================================================
param()

$ErrorActionPreference = 'Stop'
$RepoUrl = 'https://github.com/Ydjin1984/davinci-laya-zcode.git'
$PluginName = 'davinci-laya-decisions'
$Workspace = if ($env:ZCODE_PLUGIN_WORKSPACE) { $env:ZCODE_PLUGIN_WORKSPACE } else { Join-Path $env:USERPROFILE '.zcode\plugin-workspace' }
$Dest = Join-Path $Workspace "plugins\$PluginName"
$Marketplace = Join-Path $Workspace 'plugins\marketplace.json'

function Write-Info($m) { Write-Host "[PLUGIN] $m" }

# --- 1. node ------------------------------------------------------------------
$node = Get-Command node -ErrorAction SilentlyContinue
if (-not $node) {
  Write-Host "[PLUGIN] ERROR: Node.js not found. Install Node >= 20 first:" -ForegroundColor Red
  Write-Host "         winget install OpenJS.NodeJS.LTS   (or https://nodejs.org/)"
  exit 1
}
$major = [int](& node -p 'process.versions.node.split(".")[0]')
if ($major -lt 20) {
  Write-Host "[PLUGIN] ERROR: Node $major is too old; the plugin requires >= 20." -ForegroundColor Red
  exit 1
}

# --- 2. fetch the repo --------------------------------------------------------
$tmp = Join-Path $env:TEMP ("laya-plugin-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
  $git = Get-Command git -ErrorAction SilentlyContinue
  if ($git) {
    Write-Info "cloning $RepoUrl ..."
    & git clone --depth 1 -q $RepoUrl (Join-Path $tmp 'davinci-laya-zcode')
    if ($LASTEXITCODE -ne 0) { throw "git clone failed" }
  } else {
    Write-Info "git not found -- downloading the repository archive ..."
    $zip = Join-Path $tmp 'repo.zip'
    & curl.exe -fL --retry 3 -o $zip 'https://codeload.github.com/Ydjin1984/davinci-laya-zcode/zip/refs/heads/main'
    if ($LASTEXITCODE -ne 0) { throw "download failed" }
    Expand-Archive -LiteralPath $zip -DestinationPath $tmp -Force
  }

  $src = Get-ChildItem -Path $tmp -Directory -Recurse -Filter $PluginName -ErrorAction SilentlyContinue |
    Where-Object { Test-Path (Join-Path $_.FullName '.zcode-plugin\plugin.json') } |
    Select-Object -First 1
  if (-not $src) { throw "plugin directory '$PluginName' not found in the downloaded repository" }

  # --- 3. copy ----------------------------------------------------------------
  Write-Info "installing into $Dest ..."
  New-Item -ItemType Directory -Force -Path (Join-Path $Workspace 'plugins') | Out-Null
  if (Test-Path -LiteralPath $Dest) { Remove-Item -Recurse -Force $Dest }
  Copy-Item -Recurse -Force $src.FullName $Dest

  # --- 4. marketplace.json ----------------------------------------------------
  Write-Info "registering the plugin in the workspace marketplace ..."
  # The entry (names/descriptions) is taken from the repository's own
  # marketplace.json so this script stays ASCII-only and never re-types them.
  $srcMarketplace = Join-Path $src.FullName '..\..\marketplace.json'
  if (-not (Test-Path -LiteralPath $srcMarketplace)) { $srcMarketplace = '' }
  $js = @'
const fs = require("fs");
const [mpPath, dest, srcMarketplace] = process.argv.slice(2);
const read = (p) => JSON.parse(fs.readFileSync(p, "utf8").replace(/^\uFEFF/, ""));
let mp;
try { mp = read(mpPath); }
catch { mp = { name: "dev-plugin-workspace", plugins: [] }; }
if (!Array.isArray(mp.plugins)) mp.plugins = [];
fs.writeFileSync(mpPath + ".bak", JSON.stringify(mp, null, 2) + "\n");
let entry = null;
try {
  const src = read(srcMarketplace);
  entry = (src.plugins || []).find((p) => p && p.name === "davinci-laya-decisions") || null;
} catch {}
if (!entry) entry = { name: "davinci-laya-decisions", version: "0.1.0" };
entry.source = "./davinci-laya-decisions";
const i = mp.plugins.findIndex((p) => p && p.name === entry.name);
if (i >= 0) mp.plugins[i] = Object.assign({}, mp.plugins[i], entry);
else mp.plugins.push(entry);
fs.writeFileSync(mpPath, JSON.stringify(mp, null, 2) + "\n");
console.log("[PLUGIN] marketplace updated:", mpPath, "(backup: " + mpPath + ".bak)");
'@
  $jsFile = Join-Path $tmp 'marketplace.js'
  Set-Content -LiteralPath $jsFile -Value $js -Encoding ASCII
  $mpDir = Split-Path -Parent $Marketplace
  New-Item -ItemType Directory -Force -Path $mpDir | Out-Null
  & node $jsFile $Marketplace $Dest $srcMarketplace
  if ($LASTEXITCODE -ne 0) { throw "marketplace update failed" }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

# --- 5. next steps ------------------------------------------------------------
Write-Host ""
Write-Host "================= ZCode plugin installed =================" -ForegroundColor Green
Write-Host "  Plugin files: $Dest"
Write-Host "  In ZCode:"
Write-Host "    1. Settings -> Plugin Management -> refresh the marketplace list"
Write-Host "       (marketplace: dev-plugin-workspace), enable"
Write-Host "       'DaVinci x Laya: local decisions'."
Write-Host "    2. Start a NEW session (hooks, commands, skill and MCP tools load at start)."
Write-Host "    3. Approve the MCP server when asked, then check: /laya-status"
Write-Host "  Alternative (canonical) install path, no script needed:"
Write-Host "    Settings -> Plugin Management -> Discover -> '+' -> GitHub repository ->"
Write-Host "    Ydjin1984/davinci-laya-zcode  (public), then enable the plugin."
Write-Host "  The engine must be running on 127.0.0.1:8095 for active features:"
Write-Host "    start-laya.cmd        (GPU)   or   start-laya.cmd /cpu"
Write-Host "  Without the engine the plugin stays in a passive, fail-open mode."
Write-Host "========================================================="
