"""Thin client over the Dejima HTTP API.

Design note: this is a small hand-written client for the v0.x API. Once the API
stabilizes (v1.0), the request/response layer is intended to be generated from
``openapi.yaml`` at the repo root, leaving only the ergonomic helpers (notably
the ``attach`` PTY stream) hand-written.
"""

from __future__ import annotations

import os
from typing import Any, Dict, List, Optional
from urllib.parse import quote

import requests

DEFAULT_HOST = "127.0.0.1:7273"


class DejimaError(RuntimeError):
    """Raised when the daemon returns a non-2xx response."""

    def __init__(self, status: int, message: str):
        super().__init__(f"HTTP {status}: {message}")
        self.status = status
        self.message = message


class Client:
    """A client for one Dejima daemon.

    host:  ``HOST:PORT`` of the daemon (default: ``$DEJIMA_HOST`` or 127.0.0.1:7273).
    token: per-island bearer token for the autonomy path (default: ``$DEJIMA_TOKEN``).
           Operator access over the unix socket / tailnet needs no token.
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
            body = r.text.strip()
            raise DejimaError(r.status_code, body or r.reason)
        return r

    def _json(self, method: str, path: str, **kw) -> Any:
        r = self._req(method, path, **kw)
        return r.json() if r.content else None

    @staticmethod
    def _seg(s: str) -> str:
        return quote(s, safe="")

    # ----- islands --------------------------------------------------------
    def list_islands(self) -> List[Dict[str, Any]]:
        return self._json("GET", "/v1/islands")

    def create_island(
        self,
        repo: str,
        *,
        agent: Optional[str] = None,
        name: Optional[str] = None,
        image: Optional[str] = None,
        resources: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        body: Dict[str, Any] = {"repo": repo}
        if agent:
            body["agent"] = agent
        if name:
            body["name"] = name
        if image:
            body["image"] = image
        if resources:
            body["resources"] = resources
        return self._json("POST", "/v1/islands", json=body)

    def get_island(self, name: str) -> Dict[str, Any]:
        return self._json("GET", f"/v1/islands/{self._seg(name)}")

    def delete_island(self, name: str, *, force: bool = False) -> None:
        q = "?force=true" if force else ""
        self._req("DELETE", f"/v1/islands/{self._seg(name)}{q}")

    def hibernate(self, name: str) -> None:
        self._req("POST", f"/v1/islands/{self._seg(name)}/hibernate")

    def wake(self, name: str) -> None:
        self._req("POST", f"/v1/islands/{self._seg(name)}/wake")

    def reset(self, name: str) -> None:
        self._req("POST", f"/v1/islands/{self._seg(name)}/reset")

    def clone_island(self, name: str, new_name: str) -> Dict[str, Any]:
        return self._json(
            "POST", f"/v1/islands/{self._seg(name)}/clone", json={"name": new_name}
        )

    def set_resources(
        self, name: str, *, memory: Optional[str] = None, oom_priority: Optional[int] = None
    ) -> Dict[str, Any]:
        body: Dict[str, Any] = {}
        if memory is not None:
            body["memory"] = memory
        if oom_priority is not None:
            body["oom_priority"] = oom_priority
        return self._json("PUT", f"/v1/islands/{self._seg(name)}/resources", json=body)

    # ----- agents ---------------------------------------------------------
    def list_agents(self, name: str) -> List[Dict[str, Any]]:
        return self._json("GET", f"/v1/islands/{self._seg(name)}/agents")

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
        body = {
            k: v
            for k, v in dict(type=type, label=label, cmd=cmd, provider=provider, model=model).items()
            if v is not None
        }
        return self._json("POST", f"/v1/islands/{self._seg(name)}/agents", json=body)

    def remove_agent(self, name: str, agent_id: str) -> None:
        self._req("DELETE", f"/v1/islands/{self._seg(name)}/agents/{self._seg(agent_id)}")

    # ----- exec / files / logs -------------------------------------------
    def exec(self, name: str, cmd: List[str]) -> Dict[str, Any]:
        """Run a one-shot command. Returns {stdout, stderr, exit_code}."""
        return self._json("POST", f"/v1/islands/{self._seg(name)}/exec", json={"cmd": cmd})

    def read_file(self, name: str, path: str) -> bytes:
        r = self._req("GET", f"/v1/islands/{self._seg(name)}/files/{quote(path)}")
        return r.content

    def write_file(self, name: str, path: str, data: bytes) -> None:
        self._req("PUT", f"/v1/islands/{self._seg(name)}/files/{quote(path)}", data=data)

    def logs(self, name: str, *, agent: Optional[str] = None) -> str:
        params = {"agent": agent} if agent else None
        return self._req("GET", f"/v1/islands/{self._seg(name)}/logs", params=params).text

    # ----- daemon ---------------------------------------------------------
    def overview(self) -> Dict[str, Any]:
        return self._json("GET", "/v1/overview")

    def agent_types(self) -> Any:
        return self._json("GET", "/v1/agent-types")

    def healthz(self) -> bool:
        try:
            self._req("GET", "/v1/healthz")
            return True
        except DejimaError:
            return False

    def subscribe_webhook(
        self, url: str, *, secret: Optional[str] = None, events: Optional[List[str]] = None
    ) -> Any:
        body: Dict[str, Any] = {"url": url}
        if secret:
            body["secret"] = secret
        if events:
            body["events"] = events
        return self._json("POST", "/v1/events/subscribe", json=body)

    # ----- session (WebSocket PTY) ---------------------------------------
    def session_url(self, name: str, *, agent: Optional[str] = None) -> str:
        """The WebSocket URL for an interactive PTY session (multi-attach)."""
        ws = "ws" if self.base.startswith("http://") else "wss"
        base = f"{ws}://{self.host}"
        if agent:
            return f"{base}/v1/islands/{self._seg(name)}/agents/{self._seg(agent)}/session"
        return f"{base}/v1/islands/{self._seg(name)}/session"

    def attach(self, name: str, *, agent: Optional[str] = None):
        """Open the PTY session stream. Requires the `ws` extra (websocket-client).

        Returns a connected websocket._core.WebSocket; read/write raw PTY bytes.
        """
        try:
            import websocket  # type: ignore
        except ImportError as e:  # pragma: no cover
            raise DejimaError(
                0, "attach() needs the 'ws' extra: pip install 'dejima[ws]'"
            ) from e
        header = [f"Authorization: Bearer {self.token}"] if self.token else None
        return websocket.create_connection(self.session_url(name, agent=agent), header=header)
