"""Thin client over the Dejima HTTP/WebSocket API.

Design note: this is a small hand-written client for the v0.x API. The request/
response layer mirrors the route table in ``internal/api/server.go`` (the source
of truth) and the hand-maintained ``openapi.yaml`` at the repo root. Once the API
stabilizes (v1.0), the request/response layer is intended to be generated from
the spec, leaving only the ergonomic helper — the :class:`Session` PTY stream —
hand-written.

The PTY session is *not* a raw byte stream: the daemon speaks a small JSON
envelope protocol (``{"type": "data", "b64": ...}`` / ``{"type": "resize", ...}``)
over the WebSocket, base64-encoding the terminal bytes. :class:`Session` hides
that; see :meth:`Client.attach`.
"""

from __future__ import annotations

import base64
import json
import os
from typing import Any, Dict, List, Mapping, Optional
from urllib.parse import quote

import requests

DEFAULT_HOST = "127.0.0.1:7273"


class DejimaError(RuntimeError):
    """Raised when the daemon returns a non-2xx response.

    The daemon's error body is JSON (``{"error": "..."}``); ``message`` is the
    extracted text when present, else the raw body.
    """

    def __init__(self, status: int, message: str):
        super().__init__(f"HTTP {status}: {message}")
        self.status = status
        self.message = message


def _clean(d: Mapping[str, Any]) -> Dict[str, Any]:
    """Drop None-valued keys so optional fields are simply omitted from the body."""
    return {k: v for k, v in d.items() if v is not None}


class Client:
    """A client for one Dejima daemon.

    host:  ``HOST:PORT`` of the daemon (default: ``$DEJIMA_HOST`` or 127.0.0.1:7273).
    token: per-island bearer token for the autonomy path (default: ``$DEJIMA_TOKEN``).
           Operator access over the unix socket / tailnet needs no token.
    scheme: ``http`` (default) or ``https``.
    timeout: per-request timeout in seconds.
    """

    def __init__(
        self,
        host: Optional[str] = None,
        token: Optional[str] = None,
        *,
        scheme: str = "http",
        timeout: float = 30.0,
    ):
        self.host = host or os.environ.get("DEJIMA_HOST", DEFAULT_HOST)
        self.token = token or os.environ.get("DEJIMA_TOKEN")
        self.scheme = scheme
        self.base = f"{scheme}://{self.host}"
        self.timeout = timeout
        self._s = requests.Session()
        if self.token:
            self._s.headers["Authorization"] = f"Bearer {self.token}"

    # ----- low-level ------------------------------------------------------
    def _req(self, method: str, path: str, **kw) -> requests.Response:
        kw.setdefault("timeout", self.timeout)
        r = self._s.request(method, self.base + path, **kw)
        if not r.ok:
            raise DejimaError(r.status_code, _error_message(r))
        return r

    def _json(self, method: str, path: str, **kw) -> Any:
        r = self._req(method, path, **kw)
        return r.json() if r.content else None

    @staticmethod
    def _seg(s: str) -> str:
        """Percent-encode a single path segment (a name/id), escaping slashes."""
        return quote(s, safe="")

    def _island(self, name: str) -> str:
        return f"/v1/islands/{self._seg(name)}"

    # ===== islands ========================================================
    def list_islands(self) -> List[Dict[str, Any]]:
        """All islands, most-recently-used first."""
        return self._json("GET", "/v1/islands")

    def create_island(
        self,
        repo: str,
        *,
        agent: Optional[str] = None,
        name: Optional[str] = None,
        image: Optional[str] = None,
        cmd: Optional[str] = None,
        resources: Optional[Dict[str, Any]] = None,
        agents: Optional[List[Dict[str, Any]]] = None,
        role: Optional[str] = None,
        github_identity: Optional[str] = None,
        seed_path: Optional[str] = None,
        owner: Optional[str] = None,
        tags: Optional[Dict[str, str]] = None,
    ) -> Dict[str, Any]:
        """Provision an island.

        ``repo`` is a git URL or local path. ``cmd`` is required when
        ``agent="headless"``. ``agents`` seeds a multi-agent island (element 0 is
        the primary). On a token-authenticated (Home-Island) create the response
        carries the child island's ``token``.
        """
        body = _clean(
            dict(
                repo=repo,
                agent=agent,
                name=name,
                image=image,
                cmd=cmd,
                resources=resources,
                agents=agents,
                role=role,
                github_identity=github_identity,
                seed_path=seed_path,
                owner=owner,
                tags=tags,
            )
        )
        return self._json("POST", "/v1/islands", json=body)

    def get_island(self, name: str) -> Dict[str, Any]:
        """Island detail (agents, presence, cpu/mem, git, health, resources, disk)."""
        return self._json("GET", self._island(name))

    def update_island(self, name: str, *, title: str) -> Dict[str, Any]:
        """Update the island's cosmetic display title (``""`` clears it)."""
        return self._json("PATCH", self._island(name), json={"title": title})

    def delete_island(self, name: str, *, force: bool = False) -> None:
        """Purge an island (container + volumes). ``force`` bypasses the
        unpushed-work guard (which returns a 409 otherwise)."""
        self._req("DELETE", self._island(name), params={"force": "true"} if force else None)

    def hibernate(self, name: str) -> Dict[str, Any]:
        """Stop the container, preserving volumes."""
        return self._json("POST", f"{self._island(name)}/hibernate")

    def wake(self, name: str) -> Dict[str, Any]:
        """Start a hibernated island's container against existing volumes."""
        return self._json("POST", f"{self._island(name)}/wake")

    def reset(self, name: str) -> Dict[str, Any]:
        """Clear agent on-disk state (preserves the workspace)."""
        return self._json("POST", f"{self._island(name)}/reset")

    def upgrade(self, name: str) -> Dict[str, Any]:
        """Recreate the container against the current island image (both volumes
        preserved); also re-assembles bind mounts for older islands."""
        return self._json("POST", f"{self._island(name)}/upgrade")

    def clone_island(self, name: str, new_name: str) -> Dict[str, Any]:
        """Copy an island (workspace + home volumes; Port grants are dropped)."""
        return self._json("POST", f"{self._island(name)}/clone", json={"new_name": new_name})

    def set_resources(
        self, name: str, *, memory: Optional[str] = None, oom_priority: Optional[int] = None
    ) -> Dict[str, Any]:
        """Update memory limit (live) and/or OOM priority (needs a recreate; the
        response flags ``restart_required``). ``memory=""`` means unlimited."""
        body: Dict[str, Any] = {}
        if memory is not None:
            body["memory"] = memory
        if oom_priority is not None:
            body["oom_priority"] = oom_priority
        return self._json("PUT", f"{self._island(name)}/resources", json=body)

    def workspace_ready(self, name: str) -> bool:
        """Whether the island's repo clone has landed in /workspace yet."""
        r = self._json("GET", f"{self._island(name)}/workspace-ready")
        return bool(r and r.get("ready"))

    def island_events(self, name: str) -> List[Dict[str, Any]]:
        """The island's recent event log (newest first)."""
        return self._json("GET", f"{self._island(name)}/events")

    # ===== agents =========================================================
    def list_agents(self, name: str) -> List[Dict[str, Any]]:
        return self._json("GET", f"{self._island(name)}/agents")

    def add_agent(
        self,
        name: str,
        *,
        type: Optional[str] = None,
        label: Optional[str] = None,
        cmd: Optional[str] = None,
        provider: Optional[str] = None,
        model: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Add an agent (its own worktree + tmux session). ``cmd`` is required for
        a generic headless type."""
        body = _clean(dict(type=type, label=label, cmd=cmd, provider=provider, model=model))
        return self._json("POST", f"{self._island(name)}/agents", json=body)

    def get_agent(self, name: str, agent_id: str) -> Dict[str, Any]:
        return self._json("GET", f"{self._island(name)}/agents/{self._seg(agent_id)}")

    def update_agent(self, name: str, agent_id: str, *, label: str) -> Dict[str, Any]:
        """Rename an agent's cosmetic label (``""`` clears it)."""
        return self._json(
            "PATCH", f"{self._island(name)}/agents/{self._seg(agent_id)}", json={"label": label}
        )

    def remove_agent(self, name: str, agent_id: str) -> None:
        """Remove a non-primary agent (kills its session, prunes its worktree;
        the branch is kept). The primary and last agent cannot be removed."""
        self._req("DELETE", f"{self._island(name)}/agents/{self._seg(agent_id)}")

    def configure_agent(
        self,
        name: str,
        agent_id: str,
        *,
        provider: Optional[str] = None,
        model: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Set an agent's LLM provider/model (only for key-requiring frameworks).
        A change flags ``restart_required`` (selection rides a create-time env)."""
        body: Dict[str, Any] = {}
        if provider is not None:
            body["provider"] = provider
        if model is not None:
            body["model"] = model
        return self._json(
            "PATCH", f"{self._island(name)}/agents/{self._seg(agent_id)}/config", json=body
        )

    # ===== exec / files / logs -------------------------------------------
    def exec(self, name: str, cmd: List[str]) -> Dict[str, Any]:
        """Run a one-shot command. Returns ``{stdout, stderr, exit_code}``."""
        return self._json("POST", f"{self._island(name)}/exec", json={"cmd": cmd})

    def read_file(self, name: str, path: str) -> bytes:
        """Read a file from the island (defaults to the primary worktree)."""
        return self._req("GET", f"{self._island(name)}/files/{quote(path)}").content

    def write_file(self, name: str, path: str, data: bytes) -> None:
        """Write a file into the island."""
        self._req("PUT", f"{self._island(name)}/files/{quote(path)}", data=data)

    def logs(self, name: str, *, agent: Optional[str] = None) -> str:
        """Fetch container/agent logs as text."""
        return self._req("GET", f"{self._island(name)}/logs", params=_clean({"agent": agent})).text

    # ===== port broker ----------------------------------------------------
    def list_port_scopes(self, name: str) -> Dict[str, Any]:
        """List the island's brokered host-folder grants."""
        return self._json("GET", f"{self._island(name)}/port/scopes")

    def grant_port_scope(self, name: str, host_path: str, *, mode: str = "ro") -> Dict[str, Any]:
        """Grant the island scoped access to a host directory (``ro``/``rw``)."""
        return self._json(
            "POST", f"{self._island(name)}/port/scopes", json={"host_path": host_path, "mode": mode}
        )

    def revoke_port_scope(self, name: str, scope: str) -> None:
        """Drop a Port grant by its scope name."""
        self._req("DELETE", f"{self._island(name)}/port/scopes/{self._seg(scope)}")

    def port_intake(
        self, name: str, scope: str, src_rel: str, *, dest: Optional[str] = None
    ) -> Dict[str, Any]:
        """Brokered read-only import of a host file (within a scope) into the island."""
        body = _clean(dict(scope=scope, src_rel=src_rel, dest=dest))
        return self._json("POST", f"{self._island(name)}/port/intake", json=body)

    def port_export(self, name: str, src: str) -> Dict[str, Any]:
        """Brokered export of an island file out to the host-owned export staging area."""
        return self._json("POST", f"{self._island(name)}/port/export", json={"src": src})

    def port_write(self, name: str, scope: str, src: str, dest_rel: str) -> Dict[str, Any]:
        """Brokered write of an island file into a read-write Port scope on the host."""
        return self._json(
            "POST",
            f"{self._island(name)}/port/write",
            json={"scope": scope, "src": src, "dest_rel": dest_rel},
        )

    # ===== capability broker ---------------------------------------------
    def list_capability_grants(self, name: str) -> Dict[str, Any]:
        """List the host capability targets an island may invoke."""
        return self._json("GET", f"{self._island(name)}/capability/grants")

    def grant_capability(self, name: str, target: str) -> Dict[str, Any]:
        """Grant the island permission to invoke a named host capability target."""
        return self._json(
            "POST", f"{self._island(name)}/capability/grants", json={"target": target}
        )

    def revoke_capability(self, name: str, target: str) -> None:
        """Drop a capability grant by target name."""
        self._req("DELETE", f"{self._island(name)}/capability/grants/{self._seg(target)}")

    def execute_capability(
        self,
        target: str,
        *,
        island: Optional[str] = None,
        args: Optional[Dict[str, str]] = None,
    ) -> Dict[str, Any]:
        """Invoke a granted capability. Operators name ``island``; a token-
        authenticated in-island caller is pinned to its own island."""
        body = _clean(dict(target=target, island=island, args=args))
        return self._json("POST", "/v1/capabilities/execute", json=body)

    # ===== MCP broker -----------------------------------------------------
    def list_mcp_grants(self, name: str) -> Dict[str, Any]:
        """List the MCP servers an island may call."""
        return self._json("GET", f"{self._island(name)}/mcp/grants")

    def grant_mcp(self, name: str, server: str) -> Dict[str, Any]:
        """Grant an island access to a registered MCP server."""
        return self._json("POST", f"{self._island(name)}/mcp/grants", json={"server": server})

    def revoke_mcp(self, name: str, server: str) -> None:
        """Revoke an island's MCP-server grant."""
        self._req("DELETE", f"{self._island(name)}/mcp/grants/{self._seg(server)}")

    def mcp_call(
        self,
        server: str,
        method: str,
        *,
        island: Optional[str] = None,
        params: Optional[Any] = None,
    ) -> Dict[str, Any]:
        """Invoke a JSON-RPC ``method`` on a granted MCP server (deny-all; every
        call is ledgered). Operators name ``island``; a token-authenticated
        in-island caller is pinned to its own island."""
        body = _clean(dict(server=server, method=method, island=island, params=params))
        return self._json("POST", "/v1/mcp/call", json=body)

    # ===== credentials ----------------------------------------------------
    def push_claude_credentials(self, credentials_json: str) -> None:
        """Store a Claude Code credentials blob as the seed new islands clone from."""
        self._req("PUT", "/v1/credentials/claude", json={"credentials_json": credentials_json})

    def claude_credentials_status(self) -> Dict[str, Any]:
        """Whether new islands get Claude credentials, and from where (no secret)."""
        return self._json("GET", "/v1/credentials/claude")

    def list_github_identities(self) -> Dict[str, Any]:
        """List the daemon's GitHub identities (no tokens)."""
        return self._json("GET", "/v1/credentials/github")

    def put_github_identity(
        self,
        name: str,
        *,
        login: str,
        token: str,
        id: Optional[int] = None,
        host: Optional[str] = None,
        default: bool = False,
    ) -> Dict[str, Any]:
        """Add or update a named GitHub identity."""
        body = _clean(dict(login=login, token=token, id=id, host=host, default=default or None))
        return self._json("PUT", f"/v1/credentials/github/{self._seg(name)}", json=body)

    def delete_github_identity(self, name: str) -> Dict[str, Any]:
        """Remove a GitHub identity; reports islands that still referenced it."""
        return self._json("DELETE", f"/v1/credentials/github/{self._seg(name)}")

    def github_repos(self, name: str) -> Dict[str, Any]:
        """List the repositories a GitHub identity can access (fetched daemon-side)."""
        return self._json("GET", f"/v1/credentials/github/{self._seg(name)}/repos")

    def list_providers(self) -> Dict[str, Any]:
        """List the daemon's LLM provider credentials (masked hint only, no keys)."""
        return self._json("GET", "/v1/credentials/providers")

    def put_provider(
        self,
        provider: str,
        *,
        api_key: str,
        base_url: Optional[str] = None,
        env_var: Optional[str] = None,
        default: bool = False,
    ) -> Dict[str, Any]:
        """Store or update a provider key (write-only; never echoed back)."""
        body = _clean(
            dict(api_key=api_key, base_url=base_url, env_var=env_var, default=default or None)
        )
        return self._json("PUT", f"/v1/credentials/providers/{self._seg(provider)}", json=body)

    def delete_provider(self, provider: str) -> Dict[str, Any]:
        """Remove a provider credential; reports islands that still reference it."""
        return self._json("DELETE", f"/v1/credentials/providers/{self._seg(provider)}")

    # ===== operator tokens (owner-only) ----------------------------------
    def list_tokens(self) -> Dict[str, Any]:
        """List issued operator tokens (metadata only; never secrets)."""
        return self._json("GET", "/v1/tokens")

    def create_token(
        self, role: str, *, label: Optional[str] = None, islands: Optional[List[str]] = None
    ) -> Dict[str, Any]:
        """Mint an operator bearer token. ``role`` is ``owner``/``operator``/
        ``viewer``; ``islands`` scopes it (empty = all). The response carries the
        bearer ``secret`` — returned ONCE and never stored in the clear."""
        body = _clean(dict(role=role, label=label, islands=islands))
        return self._json("POST", "/v1/tokens", json=body)

    def revoke_token(self, token_id: str) -> None:
        """Revoke an operator token by id."""
        self._req("DELETE", f"/v1/tokens/{self._seg(token_id)}")

    # ===== events / webhooks ---------------------------------------------
    def list_subscriptions(self) -> List[Dict[str, Any]]:
        """List webhook subscriptions."""
        return self._json("GET", "/v1/events/subscriptions")

    def subscribe_webhook(
        self, url: str, *, secret: Optional[str] = None, events: Optional[List[str]] = None
    ) -> Dict[str, Any]:
        """Subscribe a webhook URL to state-change events (empty ``events`` = all)."""
        body = _clean(dict(url=url, secret=secret, events=events))
        return self._json("POST", "/v1/events/subscribe", json=body)

    def unsubscribe_webhook(self, subscription_id: str) -> None:
        """Remove a webhook subscription by id."""
        self._req("DELETE", f"/v1/events/subscriptions/{self._seg(subscription_id)}")

    # ===== daemon ---------------------------------------------------------
    def overview(self) -> Dict[str, Any]:
        """Daemon health, VM memory, fleet rollup."""
        return self._json("GET", "/v1/overview")

    def agent_types(self) -> Dict[str, Any]:
        """The built-in agent-type capability descriptors (provider/model hints)."""
        return self._json("GET", "/v1/agent-types")

    def healthz(self) -> bool:
        """Reachability probe; True when the daemon answers."""
        try:
            self._req("GET", "/v1/healthz")
            return True
        except DejimaError:
            return False

    def audit(
        self,
        *,
        limit: Optional[int] = None,
        since: Optional[str] = None,
        until: Optional[str] = None,
        island: Optional[str] = None,
        type_prefix: Optional[str] = None,
        actor: Optional[str] = None,
        decision: Optional[str] = None,
        format: Optional[str] = None,
    ) -> Any:
        """The audit ledger (brokered ops + ``api.request`` + lifecycle) plus a
        hash-chain verification verdict.

        Filters compose; ``limit`` tails the filtered result. ``since``/``until``
        are RFC3339; ``type_prefix`` matches the entry type prefix (e.g. ``port.``);
        ``decision`` is ``allowed``/``denied``. With ``format="jsonl"`` or
        ``"csv"`` the daemon streams a plain-text export — this returns that text
        instead of the parsed JSON envelope.
        """
        params = _clean(
            {
                "limit": limit,
                "since": since,
                "until": until,
                "island": island,
                "type": type_prefix,
                "actor": actor,
                "decision": decision,
                "format": format,
            }
        )
        if format in ("jsonl", "csv"):
            return self._req("GET", "/v1/audit", params=params).text
        return self._json("GET", "/v1/audit", params=params)

    def activity(
        self,
        *,
        actor: Optional[str] = None,
        island: Optional[str] = None,
        owner: Optional[str] = None,
        kind: Optional[str] = None,
        decision: Optional[str] = None,
        since: Optional[str] = None,
        until: Optional[str] = None,
        limit: Optional[int] = None,
    ) -> Dict[str, Any]:
        """The team activity feed — a curated, human-rendered timeline projected
        over the audit ledger (who launched what, which agent did what), newest
        first. ``kind`` is ``lifecycle``/``broker``/``system``; ``decision`` is
        ``allowed``/``denied``; ``since``/``until`` are RFC3339."""
        params = _clean(
            {
                "actor": actor,
                "island": island,
                "owner": owner,
                "kind": kind,
                "decision": decision,
                "since": since,
                "until": until,
                "limit": limit,
            }
        )
        return self._json("GET", "/v1/activity", params=params)

    def client_history(self) -> List[Dict[str, Any]]:
        """Recent attach/detach events across every island (newest first)."""
        return self._json("GET", "/v1/clients")

    def revoke_sessions(self) -> Dict[str, Any]:
        """Drop every active websocket client across every island."""
        return self._json("POST", "/v1/sessions/revoke")

    def panic_status(self) -> Dict[str, Any]:
        """Whether the daemon is in PANIC (all islands stopped, no auto-start)."""
        return self._json("GET", "/v1/panic")

    def panic(self, *, reason: Optional[str] = None) -> Dict[str, Any]:
        """Engage PANIC: stop every island."""
        return self._json("POST", "/v1/panic", json=_clean({"reason": reason}))

    def unpanic(self) -> Dict[str, Any]:
        """Clear PANIC and restart islands that should be running."""
        return self._json("DELETE", "/v1/panic")

    def admin_update(self, *, execute: bool = False, force: bool = False) -> Dict[str, Any]:
        """Report (or, with ``execute``, apply) a daemon self-update. With clients
        attached an apply defers unless ``force`` is set."""
        return self._json("POST", "/v1/admin/update", json={"execute": execute, "force": force})

    def image_build(self) -> str:
        """Rebuild the island image; returns the combined build output as text. A
        failure is reported as a trailing ``ERROR:`` line in the stream."""
        return self._req("POST", "/v1/image/build").text

    def authorize_ssh_key(self, public_key: str) -> Dict[str, Any]:
        """Authorize an OpenSSH public key fleet-wide; returns its fingerprint."""
        return self._json("POST", "/v1/ssh/account-keys", json={"public_key": public_key})

    def list_ssh_keys(self) -> Dict[str, Any]:
        """List the fleet-wide authorized SSH keys."""
        return self._json("GET", "/v1/ssh/account-keys")

    # ===== host terminals (operator-only; requires --host-terminals) -----
    def list_terminals(self) -> Dict[str, Any]:
        """List host terminals (uncontained operator shells on the daemon host)."""
        return self._json("GET", "/v1/terminals")

    def create_terminal(self, *, label: Optional[str] = None) -> Dict[str, Any]:
        """Create a host terminal."""
        return self._json("POST", "/v1/terminals", json=_clean({"label": label}))

    def delete_terminal(self, terminal_id: str) -> None:
        """Remove a host terminal."""
        self._req("DELETE", f"/v1/terminals/{self._seg(terminal_id)}")

    def relabel_terminal(self, terminal_id: str, *, label: str) -> Dict[str, Any]:
        """Rename a host terminal."""
        return self._json(
            "PATCH", f"/v1/terminals/{self._seg(terminal_id)}", json={"label": label}
        )

    # ===== session (WebSocket PTY) ---------------------------------------
    def session_url(self, name: str, *, agent: Optional[str] = None) -> str:
        """The WebSocket URL for an interactive PTY session (multi-attach)."""
        base = self._ws_base()
        if agent:
            return f"{base}{self._island(name)}/agents/{self._seg(agent)}/session"
        return f"{base}{self._island(name)}/session"

    def terminal_session_url(self, terminal_id: str, *, label: Optional[str] = None) -> str:
        """The WebSocket URL for a host terminal's PTY session."""
        url = f"{self._ws_base()}/v1/terminals/{self._seg(terminal_id)}/session"
        if label:
            url += f"?label={quote(label)}"
        return url

    def _ws_base(self) -> str:
        ws = "wss" if self.scheme == "https" else "ws"
        return f"{ws}://{self.host}"

    def attach(self, name: str, *, agent: Optional[str] = None, label: Optional[str] = None) -> "Session":
        """Open an island's PTY session stream. Requires the ``ws`` extra
        (``pip install 'dejima[ws]'``). Returns a :class:`Session`."""
        return self._open_session(self.session_url(name, agent=agent), label=label)

    def attach_terminal(self, terminal_id: str, *, label: Optional[str] = None) -> "Session":
        """Open a host terminal's PTY session stream."""
        return self._open_session(self.terminal_session_url(terminal_id, label=label), label=label)

    def _open_session(self, url: str, *, label: Optional[str]) -> "Session":
        try:
            import websocket  # type: ignore
        except ImportError as e:  # pragma: no cover
            raise DejimaError(0, "attach() needs the 'ws' extra: pip install 'dejima[ws]'") from e
        header = [f"Authorization: Bearer {self.token}"] if self.token else None
        ws = websocket.create_connection(url, header=header)
        return Session(ws)


def _error_message(r: requests.Response) -> str:
    """Extract the daemon's ``{"error": ...}`` text, falling back to body/reason."""
    body = (r.text or "").strip()
    try:
        data = r.json()
        if isinstance(data, dict) and isinstance(data.get("error"), str):
            return data["error"]
    except ValueError:
        pass
    return body or r.reason


class Session:
    """An attached PTY session over the daemon's JSON-envelope WebSocket protocol.

    The wire is line-delimited JSON envelopes, terminal bytes base64-encoded:

    - server → client: ``{"type": "hello", "attached": [...]}`` once on connect,
      then ``{"type": "data", "b64": ...}`` and ``{"type": "error", "b64": ...}``.
    - client → server: ``{"type": "data", "b64": ...}`` and
      ``{"type": "resize", "rows": R, "cols": C}``.

    :meth:`send` and :meth:`recv` deal in raw ``bytes``; the base64/envelope
    framing is handled for you. Usable as a context manager.
    """

    def __init__(self, ws: Any):
        self._ws = ws

    # context-manager sugar
    def __enter__(self) -> "Session":
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def send(self, data: bytes) -> None:
        """Send raw bytes to the PTY (keystrokes)."""
        self._send_envelope({"type": "data", "b64": base64.b64encode(data).decode("ascii")})

    def resize(self, rows: int, cols: int) -> None:
        """Resize the PTY."""
        self._send_envelope({"type": "resize", "rows": rows, "cols": cols})

    def recv(self) -> Optional[bytes]:
        """Receive the next chunk of PTY output as raw bytes.

        Skips the initial ``hello`` and any control envelopes, returning the next
        ``data`` payload. Raises :class:`DejimaError` on a server ``error``
        envelope. Returns ``None`` when the connection closes.
        """
        import websocket  # type: ignore

        while True:
            try:
                msg = self._ws.recv()
            except websocket.WebSocketConnectionClosedException:
                return None
            if msg is None or msg == "":
                return None
            if isinstance(msg, bytes):
                msg = msg.decode("utf-8", "replace")
            env = json.loads(msg)
            kind = env.get("type")
            if kind == "data":
                return base64.b64decode(env.get("b64", ""))
            if kind == "error":
                # The daemon puts the plaintext error string in the b64 field for
                # error envelopes (it is not base64-encoded there).
                raise DejimaError(0, str(env.get("b64", "")))
            # "hello" and anything unknown: keep reading.

    def close(self) -> None:
        try:
            self._ws.close()
        except Exception:  # pragma: no cover - best effort
            pass

    def _send_envelope(self, env: Dict[str, Any]) -> None:
        self._ws.send(json.dumps(env))
