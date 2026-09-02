<#
.SYNOPSIS
  Clean-machine acceptance gate for the Windows/WSL2 host path.

.DESCRIPTION
  The Windows path has never been walked end to end by anything but a person.
  Every defect in it was found by the operator, in their terminal, one round trip
  at a time — eleven of them in a single day. The reason is structural and is the
  whole design of this script:

      THE UNATTENDED CONTEXT LACKS WHAT AN INTERACTIVE ONE SUPPLIES FOR FREE.

  `wsl -d dejima -- dejimad` has PATH and HOME and works every time. The boot
  context has neither, so the daemon failed on PATH (`setsid: failed to execute
  dejimad`), then on HOME (`locate home dir: $HOME is not defined`), then on a
  stale socket — each found only after the previous was fixed, and none of them
  findable by running the command by hand. Hand-testing is precisely the
  environment that hides this class.

  So the two steps that carry the value are the ones a convenience script would
  skip: starting from NO distro, and terminating the distro and letting it come
  back with no window held open. Re-running setup on a healthy distro is the test
  that has always passed and has never proved anything.

.PARAMETER Distro
  WSL distro to build and destroy. Defaults to a SCRATCH name, deliberately.

.PARAMETER AllowRealDistro
  Required to target a distro named 'dejima'. See the safety note below.

.PARAMETER Phase
  1 (default) runs everything up to the Windows reboot. 2 runs the post-reboot
  assertions. The reboot cannot be automated from inside the run, so the state
  needed to resume is written to disk.

.PARAMETER SkipTeardown
  Keep an existing distro instead of unregistering it. This WEAKENS the gate to
  the test that always passes; the script says so in its own output.

.NOTES
  SAFETY. Step 1 runs `wsl --unregister`, which destroys the distro and every
  island volume inside it, irreversibly, with no prompt from WSL. The operator
  currently has a working host in a distro named 'dejima'. So the default target
  is 'dejima-accept', and pointing this at 'dejima' needs -AllowRealDistro typed
  on purpose. A gate that eats the thing it was meant to protect is not a gate.

  THE AUTHOR CANNOT SELF-VERIFY. No island can reach WSL; this has never been
  executed. It is operator-gated in the same way as scripts/clean-mac. A green
  run on a real Windows box is the proof — not the author's say-so, and not the
  fact that it was written carefully. See
  docs/operator-tests/clean-windows-wsl-gate.md.
#>

[CmdletBinding()]
param(
    [string] $Distro          = 'dejima-accept',
    [switch] $AllowRealDistro,
    [ValidateSet(1, 2)]
    [int]    $Phase           = 1,
    [switch] $SkipTeardown,
    [string] $IslandName      = 'accept-island'
)

$ErrorActionPreference = 'Stop'
$script:StatePath = Join-Path $env:LOCALAPPDATA 'dejima-wsl-proof.json'
$script:Failures  = 0

# --- output -----------------------------------------------------------------

function Write-Step   { param([string] $m) Write-Host "`n=== $m" -ForegroundColor Cyan }
function Write-Ok     { param([string] $m) Write-Host "  [ok]   $m" -ForegroundColor Green }
function Write-Info   { param([string] $m) Write-Host "  $m" }
function Write-Fail   {
    param([string] $m)
    $script:Failures++
    Write-Host "  [FAIL] $m" -ForegroundColor Red
}

# --- running things in the distro -------------------------------------------
#
# Every call goes through here for one reason: the exit code is read from
# $LASTEXITCODE on the wsl.exe invocation itself, never from a pipeline. d2
# committed today with "* gofmt: 1" printed directly above the commit because
# their gate was piped into `tail` and the pipeline reported tail's status. The
# command knew; the thing they read did not.
#
# Note also what is NOT here: no double quotes are placed inside the script text
# by callers. wsl.exe's argument handling has been observed eating them, which
# splits a command into extra arguments and produces errors whose $0 is a
# fragment of the command string.

function Invoke-InDistro {
    param(
        [Parameter(Mandatory)] [string] $Script,
        [switch] $AllowFailure
    )
    $out = & wsl.exe -d $Distro -- sh -c $Script 2>&1
    $code = $LASTEXITCODE
    if ($code -ne 0 -and -not $AllowFailure) {
        Write-Fail "in-distro command exited $code"
        Write-Host ($out | Out-String)
    }
    return [pscustomobject]@{ Code = $code; Output = ($out | Out-String) }
}

# --- diagnostics ------------------------------------------------------------
#
# Collected on EVERY failure, unconditionally. The daemon's fatal error sat in
# its own log for hours while nothing surfaced it. The diagnosis existed; nobody
# was looking. A gate that reports "failed" and stops has reproduced the exact
# problem it was written to end.

function Show-Diagnostics {
    param([string] $Because)

    Write-Step "DIAGNOSTICS — $Because"

    $probes = @(
        @{ Label = 'dejimad on PATH';   Cmd = 'command -v dejimad || echo NOT-ON-PATH' },
        @{ Label = 'HOME';              Cmd = 'echo HOME=$HOME' },
        @{ Label = 'socket';            Cmd = 'ls -l $HOME/.dejima/dejimad.sock 2>&1 || echo NO-SOCKET' },
        @{ Label = 'process';           Cmd = 'pgrep -a dejimad || echo NOT-RUNNING' },
        @{ Label = 'systemd';           Cmd = 'systemctl is-system-running 2>&1 || echo NO-SYSTEMD' },
        @{ Label = 'unit status';       Cmd = 'systemctl status dejimad --no-pager 2>&1 | head -20 || true' },
        @{ Label = 'journal';           Cmd = 'journalctl -u dejimad --no-pager -n 30 2>&1 || true' },
        @{ Label = 'daemon log';        Cmd = 'tail -n 40 $HOME/.dejima/dejimad.log 2>&1 || echo NO-LOG' },
        @{ Label = 'docker';            Cmd = 'docker info 2>&1 | head -5 || echo NO-DOCKER' }
    )

    foreach ($p in $probes) {
        Write-Host "`n--- $($p.Label)" -ForegroundColor Yellow
        $r = Invoke-InDistro -Script $p.Cmd -AllowFailure
        Write-Host $r.Output.TrimEnd()
    }
}

function Assert-OrDiagnose {
    param(
        [Parameter(Mandatory)] [bool]   $Condition,
        [Parameter(Mandatory)] [string] $What
    )
    if ($Condition) {
        Write-Ok $What
        return $true
    }
    Write-Fail $What
    Show-Diagnostics -Because $What
    return $false
}

# --- the durability gate ----------------------------------------------------
#
# THE STEP THAT TOOK ELEVEN ATTEMPTS. Terminate the distro so nothing is held
# open, then reach it again and require BOTH the socket and a live process.
#
# Both halves matter and neither alone is sufficient. A socket file survives a
# terminate as a stale inode with nothing behind it, so socket-only passes on a
# dead daemon. A process can be up while its socket was never recreated, so
# process-only passes on a daemon nothing can reach. The bug that cost the day
# presented as each of those in turn.

function Test-SurvivesTerminate {
    Write-Step 'Durability gate: terminate the distro, then come back'

    & wsl.exe --terminate $Distro | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Fail "wsl --terminate exited $LASTEXITCODE"
        return $false
    }
    Write-Info 'distro terminated (nothing is holding it open)'

    # Reaching in restarts the VM. This is the boot context — no interactive
    # shell, no profile, none of what a hand-run command silently inherits.
    Start-Sleep -Seconds 3

    $sock = Invoke-InDistro -Script 'test -S $HOME/.dejima/dejimad.sock' -AllowFailure
    $proc = Invoke-InDistro -Script 'pgrep -x dejimad >/dev/null 2>&1'   -AllowFailure

    $okSock = Assert-OrDiagnose -Condition ($sock.Code -eq 0) -What 'socket exists after terminate'
    $okProc = Assert-OrDiagnose -Condition ($proc.Code -eq 0) -What 'dejimad process is running after terminate'

    return ($okSock -and $okProc)
}

# --- phase 1 ----------------------------------------------------------------

function Invoke-Phase1 {
    Write-Step "Phase 1 — clean build of '$Distro'"

    if ($Distro -eq 'dejima' -and -not $AllowRealDistro) {
        throw "Refusing to touch the distro named 'dejima' without -AllowRealDistro. " +
              "Step 1 unregisters it, which destroys every island volume inside it, " +
              "irreversibly and without a prompt. Use the default scratch distro, or " +
              "pass -AllowRealDistro if you genuinely mean to destroy that host."
    }

    if ($SkipTeardown) {
        Write-Host '  [WEAKENED] -SkipTeardown: not starting from nothing.' -ForegroundColor Yellow
        Write-Host '             Re-running setup on an existing distro is the test that has' -ForegroundColor Yellow
        Write-Host '             always passed. This run cannot prove the clean-machine path.' -ForegroundColor Yellow
    } else {
        Write-Step "Unregistering '$Distro' — starting from nothing"
        & wsl.exe --unregister $Distro 2>&1 | Out-Null
        # A missing distro is the desired state, not a failure.
        Write-Info 'distro is gone (or was never there)'
    }

    Write-Step 'dejima wsl setup'
    & dejima wsl setup --distro $Distro
    if ($LASTEXITCODE -ne 0) {
        Write-Fail "dejima wsl setup exited $LASTEXITCODE"
        Show-Diagnostics -Because 'setup failed'
        return
    }
    Write-Ok 'setup exited 0'

    # Deliberately separate. `setup` exiting 0 is NOT the acceptance test, and
    # fit.txt says so in those words. Everything below is what 0 failed to mean.

    Write-Step 'Island image present'
    $img = Invoke-InDistro -Script 'docker image inspect dejima/island:latest >/dev/null 2>&1' -AllowFailure
    [void] (Assert-OrDiagnose -Condition ($img.Code -eq 0) -What 'dejima/island:latest exists on the daemon host')

    if (-not (Test-SurvivesTerminate)) {
        Write-Fail 'durability gate failed — stopping before dejima init'
        return
    }

    # --no-repo, not a repo URL: this gate is about the HOST path, and cloning
    # would drag network flakiness and credentials into an assertion about
    # whether a container starts. --name is mandatory with --no-repo (there is no
    # repo to derive one from).
    Write-Step "dejima init — a RUNNING island, which has never been proven here"
    & dejima init --name $IslandName --no-repo
    if ($LASTEXITCODE -ne 0) {
        Write-Fail "dejima init exited $LASTEXITCODE"
        Show-Diagnostics -Because 'init failed'
        return
    }

    $running = Invoke-InDistro -Script "docker ps --filter name=dejima-$IslandName --format '{{.Names}}' | grep -q ." -AllowFailure
    [void] (Assert-OrDiagnose -Condition ($running.Code -eq 0) -What "island '$IslandName' has a RUNNING container")

    # A marker inside the island's workspace, so phase 2 can prove the island
    # came back rather than merely that a container with the right name exists.
    $marker = "wsl-proof-$(Get-Random)"
    [void] (Invoke-InDistro -Script "docker exec dejima-$IslandName sh -c 'echo $marker > /workspace/.wsl-proof-marker'" -AllowFailure)

    @{ Distro = $Distro; IslandName = $IslandName; Marker = $marker } |
        ConvertTo-Json | Set-Content -Path $script:StatePath -Encoding utf8
    Write-Ok "state written to $script:StatePath"

    Write-Step 'REBOOT WINDOWS NOW, then re-run with -Phase 2'
    Write-Host '  The reboot cannot be automated from inside this run. Nothing has proven'
    Write-Host '  the daemon survives a host restart, which is the last unproven claim.'
}

# --- phase 2 ----------------------------------------------------------------

function Invoke-Phase2 {
    Write-Step 'Phase 2 — after a Windows reboot'

    if (-not (Test-Path $script:StatePath)) {
        throw "No state at $script:StatePath. Run -Phase 1 first, on this machine."
    }
    $state      = Get-Content $script:StatePath -Raw | ConvertFrom-Json
    $script:Distro = $state.Distro
    $island     = $state.IslandName
    $marker     = $state.Marker

    Write-Info "distro=$Distro island=$island"

    # No `wsl wsl setup`, no `wsl wsl start`, nothing warmed by hand. Reaching in
    # cold is the whole assertion.
    $sock = Invoke-InDistro -Script 'test -S $HOME/.dejima/dejimad.sock' -AllowFailure
    [void] (Assert-OrDiagnose -Condition ($sock.Code -eq 0) -What 'socket exists after a Windows reboot')

    $proc = Invoke-InDistro -Script 'pgrep -x dejimad >/dev/null 2>&1' -AllowFailure
    [void] (Assert-OrDiagnose -Condition ($proc.Code -eq 0) -What 'dejimad is running after a Windows reboot')

    & dejima ls | Out-Null
    [void] (Assert-OrDiagnose -Condition ($LASTEXITCODE -eq 0) -What 'the Windows client can reach the daemon after a reboot')

    $found = Invoke-InDistro -Script "docker exec dejima-$island cat /workspace/.wsl-proof-marker 2>/dev/null | grep -q $marker" -AllowFailure
    [void] (Assert-OrDiagnose -Condition ($found.Code -eq 0) -What "island '$island' survived with its workspace marker intact")
}

# --- main -------------------------------------------------------------------

if (-not (Get-Command dejima -ErrorAction SilentlyContinue)) {
    throw 'dejima is not on PATH. Install the client first, and open a NEW PowerShell — ' +
          'the installer writes PATH to the registry and a running process cannot see it.'
}

switch ($Phase) {
    1 { Invoke-Phase1 }
    2 { Invoke-Phase2 }
}

Write-Step 'RESULT'
if ($script:Failures -eq 0) {
    Write-Host "  PASS — phase $Phase clean" -ForegroundColor Green
    exit 0
}
Write-Host "  FAIL — $($script:Failures) assertion(s) failed in phase $Phase" -ForegroundColor Red
exit 1
