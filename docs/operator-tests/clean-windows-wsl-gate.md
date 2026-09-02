# Clean-Windows WSL gate — the acceptance test nobody has ever run

`scripts/clean-win/wsl-proof.ps1` is the hand-off. This page is why it exists,
what a green run does and does not prove, and the two ways to run it wrong.

## Why

No automated test has ever walked the Windows/WSL2 host path end to end. Not
once. Every defect in it was found by the operator, in their terminal, one round
trip at a time — eleven in a single day.

That is not carelessness, it is structural, and it is the design of the script:

> **The unattended context lacks what an interactive one supplies for free.**

`wsl -d dejima -- dejimad` has `PATH` and `HOME` and works every single time. The
boot context has neither. So the daemon failed on `PATH` (`setsid: failed to
execute dejimad`), then on `HOME` (`locate home dir: $HOME is not defined`), then
on a stale socket — each discovered only after the previous was fixed, and **none
of them findable by running the command by hand**. Hand-testing is precisely the
environment that hides this class of bug.

## What the script actually proves

Two of its steps carry all of the value, and both are the kind a convenience
script would skip:

1. **Starting from no distro.** `wsl --unregister` first. Re-running `dejima wsl
   setup` on a healthy distro is the test that has always passed and has never
   proved anything.
2. **Terminate and return.** `wsl --terminate`, then reach back in with no window
   held open, and require **both** a socket and a live process. Neither alone is
   sufficient: a socket file survives a terminate as a stale inode with nothing
   behind it, and a process can be up whose socket was never recreated. The bug
   that cost the day presented as each of those in turn.

Then the claim that has never been true yet: **`dejima init` producing a RUNNING
island**. `setup` exiting 0 is not the acceptance test. `fit.txt` says so in
those words.

Phase 2, after a Windows reboot, checks the daemon is still there and the island
came back — with a marker written into its workspace, so it proves the *island*
returned rather than that a container with the right name exists.

## Getting to a virgin machine first

The script rebuilds a *distro*. To re-test the **documented path from step one** —
`wsl --install --no-distribution`, the reboot, the installer, `dejima wsl setup` —
you need WSL itself gone, and that is where the operator spent an hour finding
out the published removal steps are incomplete.

Order matters: uninstall the client while its binary still exists to tell you
things, then the distro, then the feature.

```powershell
dejima uninstall --client
Remove-Item -Recurse -Force $env:LOCALAPPDATA\dejima
Remove-Item -Recurse -Force $env:USERPROFILE\.dejima -EA SilentlyContinue
$p = [Environment]::GetEnvironmentVariable('Path','User')
[Environment]::SetEnvironmentVariable('Path',
    (($p -split ';' | Where-Object { $_ -ne "$env:LOCALAPPDATA\dejima" }) -join ';'), 'User')
[Environment]::SetEnvironmentVariable('DEJIMA_HOST', $null, 'User')
```

```powershell
wsl --terminate <distro>
wsl --unregister <distro>     # deletes ext4.vhdx and every island volume in it
```

Then, **elevated**, and note `/norestart` means the removal applies at boot:

```powershell
dism.exe /online /disable-feature /featurename:Microsoft-Windows-Subsystem-Linux /norestart
dism.exe /online /disable-feature /featurename:VirtualMachinePlatform /norestart
```

### The step nothing documents, and the reason this section exists

After all of that, **File Explorer still shows a "Linux" entry in the sidebar**,
and it survives the reboot. It is a shell registration that neither
`--unregister` nor disabling the optional components removes:

```powershell
Get-Item 'HKLM:\SOFTWARE\Classes\CLSID\{B2B4A4D1-2754-4140-A2EB-9A76D9D7CDC6}'
#   (default)                      : Linux
#   System.IsPinnedToNameSpaceTree : 1
```

Elevated, with a backup first:

```powershell
reg export "HKLM\SOFTWARE\Classes\CLSID\{B2B4A4D1-2754-4140-A2EB-9A76D9D7CDC6}" "$env:USERPROFILE\wsl-nav-key-backup.reg" /y
Remove-Item 'HKLM:\SOFTWARE\Classes\CLSID\{B2B4A4D1-2754-4140-A2EB-9A76D9D7CDC6}' -Recurse -Force
Stop-Process -Name explorer -Force
```

**Why it only sometimes appears**, because that difference is what made it hard
to believe the machine was clean: an earlier wipe on the same box left no such
entry. That WSL had been installed as the **Store package**, which takes its
Explorer registration with it when uninstalled. This one was the **inbox optional
component**, where `winget uninstall --id Microsoft.WSL` reports *no installed
package found* and the registration is left behind. Same end state, two removal
paths, and only one of them self-cleans.

Nothing is behind the icon — the data really is gone by that point. But a visible
"Linux" drive is reasonable evidence that a wipe is incomplete, and telling
someone to ignore it is worse than spending five minutes removing it.

`wsl.exe` itself never goes away. Windows ships the stub in System32 regardless,
which is the same fact that made `wsl.Available()` return true on a machine with
no WSL — see `internal/wsl`.

### Verify

```powershell
wsl --status          # should report the optional components are disabled
Get-Command dejima -All
Test-Path $env:LOCALAPPDATA\dejima
[Environment]::GetEnvironmentVariable('DEJIMA_HOST','User')
Get-WindowsOptionalFeature -Online | Where-Object FeatureName -match 'Linux|VirtualMachine' | Select-Object FeatureName, State
```

The last one needs elevation and is the only check that distinguishes `Disabled`
from `DisablePending`.

## Running it

```powershell
# Phase 1 — clean build. Uses a SCRATCH distro by default.
.\scripts\clean-win\wsl-proof.ps1

# reboot Windows

# Phase 2 — post-reboot assertions
.\scripts\clean-win\wsl-proof.ps1 -Phase 2
```

Failures print diagnostics automatically: `dejimad` on PATH, `HOME`, socket,
process, systemd state, unit status, journal, the tail of `dejimad.log`, and
Docker. This is deliberate — the daemon's fatal error sat in its own log for
hours while nothing surfaced it. A gate that reports "failed" and stops has
reproduced the problem it was written to end.

## The two ways to run it wrong

**Pointing it at your real distro.** Step 1 runs `wsl --unregister`, which
destroys the distro and every island volume in it, irreversibly, with no prompt
from WSL. The default target is `dejima-accept`; targeting `dejima` requires
`-AllowRealDistro` typed on purpose. A gate that eats the host it was meant to
protect is not a gate.

**`-SkipTeardown`.** It exists for iterating on a failure, and it removes the
first of the two steps that matter. The script says so in yellow while it runs.
A green run with that flag is not a clean-machine result and should not be
reported as one.

## Status: NEVER EXECUTED

This script has not been run. It cannot be: no island can reach WSL, so the
author cannot self-verify, exactly as with `scripts/clean-mac`. **A green run on
a real Windows box is the proof — not the author's say-so, and not the fact that
it was written carefully.**

Until then, the honest statement about the Windows path is the one `fit.txt`
carries: the operator observed it working on their machine, and nobody who
wrote it has run it.

Two specific things the first real run is most likely to catch, recorded so the
first failure is not a surprise:

- **The PowerShell has never been parsed.** It was written in a Linux container
  with no `pwsh` available, so it has not been syntax-checked, let alone run. A
  first-run syntax error is the most likely outcome and says nothing about the
  logic.
- **The CLI flags are read from source, not exercised.** `dejima wsl setup
  --distro` and `dejima init --name <n> --no-repo` are both real — the first
  draft of this script used `dejima init <name> --yes`, and neither of those
  exists: `init` takes no positional name and has no `--yes`. That was caught by
  reading `cmd/dejima/main.go` rather than by running anything, which is the
  standing limit here.

Report failures with the diagnostics block the script prints; it is designed so
that block is the whole bug report.
