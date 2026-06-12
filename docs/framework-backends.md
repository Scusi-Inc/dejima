# Dejima as a framework backend (SSH)

This doc is for the **inverse** of [`agent-adapters.md`](agent-adapters.md). That
doc is about running an agent *inside* a Dejima island. This one is about
pointing an **external orchestration framework or editor** at a Dejima island as
a remote **SSH backend** — so Hermes, Goose, VS Code/Cursor Remote-SSH, or any
tool with an SSH transport can drive an island without a bespoke `dejima://`
integration.

It works because the daemon ships an **SSH-façade**: `dejimad` is the single SSH
front door for every island. The SSH **username names the island**, a
**per-island public key** authorizes the connection, and the session is bridged
into the container via `docker exec`. Islands run no sshd and publish no ports —
the daemon brokers access exactly as it does for `dejima connect`, so this is
identical on Linux and macOS and preserves containment.

> **Why SSH and not a Docker-daemon façade?** See
> [`port-island-spec.md` §5](port-island-spec.md): emulating the Docker Engine
> API semantically lies (frameworks expect ephemeral `--rm` containers; islands
> are deliberately persistent and hibernate). An SSH endpoint is a small,
> faithful surface every framework already supports. Built; the Docker façade is
> rejected.

---

## Setup (once per host + island)

1. **Start the listener** on the daemon host. Prefer a tailnet address so the
   port isn't on the open LAN (per-key auth still protects it either way):

   ```bash
   dejimad --ssh 100.x.y.z:2222          # or DEJIMAD_SSH=…; ":2222" binds all interfaces
   ```

2. **Authorize your client's public key** for the island (host-local op — run it
   where `dejimad` runs):

   ```bash
   dejima ssh authorize <island> --key ~/.ssh/id_ed25519.pub
   #   or:  dejima ssh authorize <island> "$(cat ~/.ssh/id_ed25519.pub)"
   #   or:  cat ~/.ssh/id_ed25519.pub | dejima ssh authorize <island>
   dejima ssh info <island>              # prints the connect string + host-key fingerprint
   ```

3. **Connect.** The username is the island name:

   ```bash
   ssh <island>@<daemon-host> -p 2222    # lands in the container as `dejima`, cwd $HOME
   ssh <island>@<daemon-host> -p 2222 -- ls /workspace   # one-shot exec, real exit code
   sftp -P 2222 <island>@<daemon-host>   # file transfer, rooted in the container
   ```

The daemon presents one host key for every island; pin it from
`dejima ssh info`'s fingerprint (`known_hosts`).

---

## The connection parameters every SSH backend needs

Whatever the framework calls its fields, an SSH backend reduces to these — map
them onto the framework's config:

| Parameter | Value |
|---|---|
| Host | the daemon host (its tailnet name/IP, or LAN address) |
| Port | the `--ssh` port (e.g. `2222`) |
| User | **the island name** (this is how the daemon selects the island) |
| Identity / private key | the key whose public half you `dejima ssh authorize`d |
| Known-hosts / host key | the fingerprint from `dejima ssh info` (one key for all islands) |
| Remote workdir | `/workspace` (the island's repo); `$HOME` is `/home/dejima` |

### Hermes / Goose and friends

Both ship a generic SSH backend. Configure it with the table above —
`host`/`port`/`user=<island>`/`identity_file` — and the framework will open
exec channels into the island like any remote machine. Nothing Dejima-specific
is required on the framework side; that's the point of the façade. (If a
framework wants a thin first-class "Dejima" backend later, it's a small wrapper
that fills these fields from `dejima ls` — but the generic SSH backend already
works today.)

### VS Code / Cursor / Zed / VSCodium (Remote-SSH)

Add an SSH host entry and open it:

```sshconfig
# ~/.ssh/config
Host my-island
    HostName <daemon-host>
    Port 2222
    User <island>
    IdentityFile ~/.ssh/id_ed25519
```

Then "Remote-SSH: Connect to Host… → my-island". The editor's remote server
bootstraps over the exec channel; open `/workspace` to edit the island's repo
beside the in-island agent. File-explorer operations use the sftp subsystem,
which is bridged to the island's `sftp-server`. This is plain SSH, so it works
across VS Code forks with no proprietary-extension licensing.

---

## What you get, and the containment boundary

- **You land *inside* the island** as the `dejima` user — same filesystem,
  tools, and git worktree the in-island agent sees. `/workspace` is the repo.
- **Host files stay brokered.** SSH gives you the *container*, not the host.
  Reaching host files is still governed by the **Port** (`dejima port grant` /
  `intake` / `export` / `write`) and its audit ledger — SSH does not widen that
  boundary.
- **Per-island isolation holds.** A key authorized for island A cannot open
  island B (the username must match, and the key must be in that island's
  `authorized_keys`). One compromised key scopes to one island.
- **No host shell.** The bridge runs `docker exec` with argv (no host shell), so
  a remote command executes in the container, never on the host.

## Limitations / not yet

- **Agent/X11 forwarding** isn't bridged (the façade serves session channels:
  shell, exec, pty, sftp). Port-forwarding/`direct-tcpip` is not offered by
  design — that would be an un-brokered tunnel out of the island.
- **One island = one SSH user.** Multiple agents share the island; SSH drops you
  into a login shell, not a specific agent's tmux session (use
  `dejima connect <island> --agent <id>` for that).
- **Image rebuild for sftp on old islands.** `sftp-server` is baked into the
  island image; islands created before it need `dejima image build` + an upgrade.
