# Dejima client installer (Windows / PowerShell) — drops the `dejima.exe`
# CLI from a published GitHub Release. For Windows PCs driving a *remote*
# daemon; no Go, no Docker, no daemon. The full server stack is Unix-only.
#
# Usage (in PowerShell):
#   irm https://aoos.github.io/dejima/install-client.ps1 | iex
#
# Knobs (set before running):
#   $env:DEJIMA_VERSION    release tag to install (default: latest, e.g. v0.1.0)
#   $env:DEJIMA_PREFIX     install dir (default: $env:LOCALAPPDATA\dejima)

$ErrorActionPreference = 'Stop'

function Write-Bold($msg) { Write-Host $msg -ForegroundColor White }
function Write-Info($msg) { Write-Host "  $msg" }
function Fail($msg) {
  Write-Host "[X] $msg" -ForegroundColor Red
  exit 1
}

Write-Bold "Dejima client installer (Windows)"

# --- detect arch ----------------------------------------------------------
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { Fail "unsupported arch: $env:PROCESSOR_ARCHITECTURE" }
}
Write-Info "platform: windows/$arch"

# --- resolve version ------------------------------------------------------
$repo = 'aoos/dejima'
$ver = $env:DEJIMA_VERSION
if (-not $ver) {
  Write-Info "resolving latest release..."
  try {
    $rel = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest" `
      -Headers @{ 'User-Agent' = 'dejima-installer' }
    $ver = $rel.tag_name
  } catch {
    Fail "could not reach GitHub API: $_"
  }
  if (-not $ver) {
    Fail "no published releases yet -- set `$env:DEJIMA_VERSION, or build from source"
  }
}
Write-Info "version:  $ver"

$asset = "dejima_${ver}_windows_${arch}.zip"
$base  = "https://github.com/$repo/releases/download/$ver"

# --- download + verify ----------------------------------------------------
$tmp = Join-Path $env:TEMP "dejima-install-$([guid]::NewGuid())"
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
  Write-Info "downloading $asset"
  try {
    Invoke-WebRequest "$base/$asset" -OutFile "$tmp\$asset" -UseBasicParsing
  } catch {
    Fail "download failed: $base/$asset ($_)"
  }

  # Optional checksum verify against SHA256SUMS.
  try {
    Invoke-WebRequest "$base/SHA256SUMS" -OutFile "$tmp\SHA256SUMS" -UseBasicParsing -ErrorAction Stop
    $line = Select-String -Path "$tmp\SHA256SUMS" -Pattern " $asset$" -SimpleMatch
    if ($line) {
      $want = ($line.Line -split '\s+')[0]
      $got  = (Get-FileHash "$tmp\$asset" -Algorithm SHA256).Hash.ToLower()
      if ($want.ToLower() -ne $got) {
        Fail "checksum mismatch for $asset (want $want, got $got)"
      }
      Write-Info "checksum OK"
    }
  } catch {
    Write-Info "warning: SHA256SUMS not found; skipping checksum verification"
  }

  # --- extract ------------------------------------------------------------
  $prefix = if ($env:DEJIMA_PREFIX) { $env:DEJIMA_PREFIX } else { Join-Path $env:LOCALAPPDATA 'dejima' }
  New-Item -ItemType Directory -Path $prefix -Force | Out-Null
  Expand-Archive -Path "$tmp\$asset" -DestinationPath $prefix -Force
  Write-Bold "Installed dejima.exe -> $prefix\dejima.exe"

  # --- add to PATH (User scope, persistent) ------------------------------
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $entries  = if ($userPath) { $userPath -split ';' } else { @() }
  if ($entries -notcontains $prefix) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$prefix", 'User')
    Write-Info "added $prefix to User PATH"
  } else {
    Write-Info "$prefix already on User PATH"
  }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Bold "Next steps"
Write-Host @"

  1. Find your server's tailnet address. On the Mac mini / Linux host, run:

       tailscale ip -4         # gives 100.x.y.z (foolproof)
       tailscale status --self # gives the MagicDNS name

  2. In a NEW PowerShell window, set DEJIMA_HOST and open the TUI:

       [Environment]::SetEnvironmentVariable("DEJIMA_HOST", "100.x.y.z:7273", "User")
       dejima

  (Close + reopen PowerShell after step 2 so the env var takes effect.)

  No daemon installed here -- this is the client. The full server stack
  (install.sh) is Unix-only.
"@
