# Dejima client installer (Windows / PowerShell) — drops the `dejima.exe`
# CLI from a published GitHub Release. For Windows PCs driving a *remote*
# daemon; no Go, no Docker, no daemon. The full server stack is Unix-only.
#
# Usage (in PowerShell):
#   irm https://dejima.tech/install-client.ps1 | iex
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

# --- Where will the islands run? -----------------------------------------
# Asked FIRST, because it decides whether the next two sections happen at all.
#
# Without it this script assumed a remote server: it installed Tailscale, asked
# for a tailnet address, and closed by saying the server stack is Unix-only and
# to go install it on a Mac mini. A Windows operator whose islands run in WSL2 on
# the SAME machine was walked through networking they did not need, asked for an
# address that does not exist, told to leave it blank, and then pointed at
# another computer. `dejima wsl setup` was never mentioned.
#
# One question removes all of that for the local case.
$localHost = $false
if ([Environment]::UserInteractive -and -not $env:DEJIMA_HOST) {
  Write-Host ""
  Write-Bold "Where will your islands run?"
  Write-Info "  [1] On this machine, inside WSL2  (Dejima builds a local Linux host)"
  Write-Info "  [2] On another machine  (a Mac mini or Linux box already running Dejima)"
  $where = Read-Host "Choose [1/2] (default 1)"
  if (-not $where -or $where -match '^1') { $localHost = $true }
}

# --- Tailscale ------------------------------------------------------------
# The network the client uses to reach the daemon. Sign-in is GUI-driven on
# Windows, so we install the package and point the user at the tray icon.
Write-Host ""
$haveTS = $false
if (-not $localHost) {
Write-Bold "Tailscale"
if (Get-Command tailscale -ErrorAction SilentlyContinue) {
  Write-Info "tailscale CLI found"
  $haveTS = $true
} else {
  Write-Info "Tailscale not found"
  Write-Info "Tailscale is how this client reaches your Dejima server."
  $install = $true
  if ($env:AUTO_INSTALL_TS -ne '1' -and [Environment]::UserInteractive) {
    $reply = Read-Host "Install Tailscale now via winget? [Y/n]"
    if ($reply -and ($reply -notmatch '^[Yy]')) { $install = $false }
  }
  if ($install) {
    if (Get-Command winget -ErrorAction SilentlyContinue) {
      Write-Info "Running: winget install Tailscale.Tailscale"
      try {
        winget install --id Tailscale.Tailscale --accept-source-agreements --accept-package-agreements -e
        $haveTS = $true
      } catch {
        Write-Info "winget install failed: $_"
        Write-Info "Install manually from https://tailscale.com/download/windows"
      }
    } else {
      Write-Info "winget not available — install manually from https://tailscale.com/download/windows"
    }
  }
}

if ($haveTS) {
  Write-Info "Sign in to Tailscale via the system-tray icon (right-click -> Log in)."
  Write-Info "Use the SAME tailnet as your Dejima server."
}
}  # end: remote-server branch (Tailscale is not needed for a WSL host)

# --- DEJIMA_HOST ----------------------------------------------------------
# Prompt for the server's tailnet IP/hostname; probe :7273; persist as a
# User-scope environment variable so future PowerShell sessions inherit it.
$candidate = $null
if (-not $localHost) {
Write-Host ""
Write-Bold "Server address"
Write-Info "On the SERVER (mac mini / linux box), run 'tailscale ip -4' to find its address."
Write-Info "Example: 100.84.12.7"

if ([Environment]::UserInteractive) {
  $serverHost = Read-Host "Enter your server's tailnet IP or hostname (blank to skip)"
  if ($serverHost) {
    $serverHost = $serverHost -replace ':\d+$',''  # strip user-supplied port
    $candidate  = "${serverHost}:7273"
    Write-Info "Probing $candidate..."
    try {
      $probe = Test-NetConnection -ComputerName $serverHost -Port 7273 -InformationLevel Quiet -WarningAction SilentlyContinue
      if ($probe) {
        Write-Host "  [OK] reached $candidate" -ForegroundColor Green
      } else {
        Write-Host "  [!] couldn't reach $candidate -- saving anyway" -ForegroundColor Yellow
        Write-Info "    (server may be down, or Tailscale not connected yet)"
      }
    } catch {
      Write-Host "  [!] probe failed: $_ -- saving anyway" -ForegroundColor Yellow
    }
    [Environment]::SetEnvironmentVariable('DEJIMA_HOST', $candidate, 'User')
    Write-Info "saved DEJIMA_HOST=$candidate to User environment"
  }
}

}  # end: remote-server branch

Write-Host ""
Write-Bold "Next steps"
if ($localHost) {
  Write-Host @"

  Close + reopen PowerShell so the new PATH is picked up. Then build the
  local host -- this installs Docker and the Dejima daemon inside a WSL2
  distro on this machine:

       dejima wsl setup

  First run takes a while (it creates the distro and builds an image).
  After it finishes:

       dejima              # opens the dashboard

  If WSL is not installed yet, an ADMINISTRATOR PowerShell:

       wsl --install --no-distribution
       (older wsl.exe: wsl --install, reboot, then wsl --update)

  then reboot and run 'dejima wsl setup'.
"@
} elseif ($candidate) {
  Write-Host @"

  Close + reopen PowerShell so the new PATH and DEJIMA_HOST are picked up.
  Then:

       dejima              # opens the dashboard
       dejima connect <island>

  (No daemon installed here -- this is the client. The full server stack
   is Unix-only; install on a Mac mini or Linux box with install.sh.)
"@
} else {
  Write-Host @"

  1. Set DEJIMA_HOST (one-time, persistent):

       [Environment]::SetEnvironmentVariable("DEJIMA_HOST", "100.x.y.z:7273", "User")

  2. Close + reopen PowerShell, then:

       dejima              # opens the dashboard
       dejima connect <island>

  (No daemon installed here -- this is the client. The full server stack
   is Unix-only; install on a Mac mini or Linux box with install.sh.)
"@
}
