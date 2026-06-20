"""Tests for the Dejima REST client.

These run against a stdlib ``http.server`` stub on localhost — no network, no
running daemon — so they exercise the real ``requests`` path (URLs, methods,
bodies, query params, headers, error decoding) end to end.
"""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from dejima import Client, DejimaError


class _Recorder:
    """Captures the last request and serves a scripted response."""

    def __init__(self):
        self.method = None
        self.path = None
        self.body = None
        self.headers = {}
        # response script: (status, body-bytes, content-type)
        self.status = 200
        self.resp = b"{}"
        self.content_type = "application/json"


@pytest.fixture()
def server():
    rec = _Recorder()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *a):  # silence
            pass

        def _capture(self):
            rec.method = self.command
            rec.path = self.path
            rec.headers = {k.lower(): v for k, v in self.headers.items()}
            n = int(self.headers.get("Content-Length", 0) or 0)
            rec.body = self.rfile.read(n) if n else b""

        def _respond(self):
            self.send_response(rec.status)
            self.send_header("Content-Type", rec.content_type)
            self.send_header("Content-Length", str(len(rec.resp)))
            self.end_headers()
            if rec.resp:
                self.wfile.write(rec.resp)

    # One body for every verb: capture the request, serve the scripted response.
    def _handle(self):
        self._capture()
        self._respond()

    for verb in ("GET", "POST", "PUT", "PATCH", "DELETE"):
        setattr(Handler, f"do_{verb}", _handle)

    httpd = HTTPServer(("127.0.0.1", 0), Handler)
    port = httpd.server_address[1]
    t = threading.Thread(target=httpd.serve_forever, daemon=True)
    t.start()
    try:
        yield rec, Client(host=f"127.0.0.1:{port}", token="tok-123")
    finally:
        httpd.shutdown()


def _set(rec, *, status=200, body=None):
    rec.status = status
    rec.resp = json.dumps(body).encode() if body is not None else b""


def test_auth_header_present(server):
    rec, dj = server
    _set(rec, body=[])
    dj.list_islands()
    assert rec.headers["authorization"] == "Bearer tok-123"


def test_list_islands(server):
    rec, dj = server
    _set(rec, body=[{"name": "foo"}])
    out = dj.list_islands()
    assert rec.method == "GET" and rec.path == "/v1/islands"
    assert out == [{"name": "foo"}]


def test_create_island_omits_none(server):
    rec, dj = server
    _set(rec, status=201, body={"name": "foo"})
    dj.create_island("git@github.com:you/foo.git", agent="claude-code", role="home")
    assert rec.method == "POST" and rec.path == "/v1/islands"
    sent = json.loads(rec.body)
    assert sent == {"repo": "git@github.com:you/foo.git", "agent": "claude-code", "role": "home"}
    assert "name" not in sent and "image" not in sent  # None fields dropped


def test_create_island_multi_agent(server):
    rec, dj = server
    _set(rec, status=201, body={"name": "foo"})
    dj.create_island("repo", agents=[{"type": "claude-code"}, {"type": "codex"}], tags={"team": "web"})
    sent = json.loads(rec.body)
    assert sent["agents"] == [{"type": "claude-code"}, {"type": "codex"}]
    assert sent["tags"] == {"team": "web"}


def test_get_island_encodes_segment(server):
    rec, dj = server
    _set(rec, body={"name": "a/b"})
    dj.get_island("a/b")
    assert rec.path == "/v1/islands/a%2Fb"


def test_delete_island_force(server):
    rec, dj = server
    _set(rec, status=204)
    dj.delete_island("foo", force=True)
    assert rec.method == "DELETE"
    assert rec.path == "/v1/islands/foo?force=true"


def test_delete_island_no_force(server):
    rec, dj = server
    _set(rec, status=204)
    dj.delete_island("foo")
    assert rec.path == "/v1/islands/foo"


def test_set_resources(server):
    rec, dj = server
    _set(rec, body={"resources": {}, "restart_required": True})
    out = dj.set_resources("foo", memory="4g", oom_priority=100)
    assert rec.method == "PUT" and rec.path == "/v1/islands/foo/resources"
    assert json.loads(rec.body) == {"memory": "4g", "oom_priority": 100}
    assert out["restart_required"] is True


def test_set_resources_unlimited_memory(server):
    rec, dj = server
    _set(rec, body={"resources": {}, "restart_required": False})
    dj.set_resources("foo", memory="")  # "" is a real value, not "unchanged"
    assert json.loads(rec.body) == {"memory": ""}


def test_workspace_ready(server):
    rec, dj = server
    _set(rec, body={"ready": True})
    assert dj.workspace_ready("foo") is True
    assert rec.path == "/v1/islands/foo/workspace-ready"


def test_exec(server):
    rec, dj = server
    _set(rec, body={"stdout": "ok", "stderr": "", "exit_code": 0})
    out = dj.exec("foo", ["git", "status"])
    assert json.loads(rec.body) == {"cmd": ["git", "status"]}
    assert out["exit_code"] == 0


def test_read_write_file(server):
    rec, dj = server
    rec.status = 200
    rec.resp = b"file-bytes"
    rec.content_type = "application/octet-stream"
    assert dj.read_file("foo", "src/main.go") == b"file-bytes"
    assert rec.path == "/v1/islands/foo/files/src/main.go"

    _set(rec, status=204)
    dj.write_file("foo", "x.txt", b"hi")
    assert rec.method == "PUT" and rec.body == b"hi"


def test_logs_with_agent(server):
    rec, dj = server
    rec.status = 200
    rec.resp = b"log lines"
    rec.content_type = "text/plain"
    assert dj.logs("foo", agent="p2") == "log lines"
    assert rec.path == "/v1/islands/foo/logs?agent=p2"


def test_add_agent_clean_body(server):
    rec, dj = server
    _set(rec, status=201, body={"id": "p2"})
    dj.add_agent("foo", type="codex", label="reviewer")
    assert json.loads(rec.body) == {"type": "codex", "label": "reviewer"}


def test_configure_agent(server):
    rec, dj = server
    _set(rec, body={"provider": "anthropic", "model": "anthropic/opus", "restart_required": True})
    out = dj.configure_agent("foo", "p1", provider="anthropic", model="opus")
    assert rec.path == "/v1/islands/foo/agents/p1/config"
    assert json.loads(rec.body) == {"provider": "anthropic", "model": "opus"}
    assert out["restart_required"] is True


def test_port_grant_and_revoke(server):
    rec, dj = server
    _set(rec, status=201, body={"name": "scope-1"})
    dj.grant_port_scope("foo", "/Users/me/data", mode="ro")
    assert json.loads(rec.body) == {"host_path": "/Users/me/data", "mode": "ro"}

    _set(rec, status=204)
    dj.revoke_port_scope("foo", "scope-1")
    assert rec.method == "DELETE" and rec.path == "/v1/islands/foo/port/scopes/scope-1"


def test_port_intake(server):
    rec, dj = server
    _set(rec, body={"bytes": 10})
    dj.port_intake("foo", "scope-1", "a.csv")
    assert json.loads(rec.body) == {"scope": "scope-1", "src_rel": "a.csv"}


def test_capability_execute_clean(server):
    rec, dj = server
    _set(rec, body={"ok": True, "exit_code": 0})
    dj.execute_capability("notify", args={"msg": "hi"})
    assert rec.path == "/v1/capabilities/execute"
    assert json.loads(rec.body) == {"target": "notify", "args": {"msg": "hi"}}


def test_subscribe_webhook(server):
    rec, dj = server
    _set(rec, status=201, body={"id": "sub-1"})
    dj.subscribe_webhook("https://h/x", events=["island.created"])
    assert json.loads(rec.body) == {"url": "https://h/x", "events": ["island.created"]}


def test_put_provider_default_flag(server):
    rec, dj = server
    _set(rec, body={"providers": []})
    dj.put_provider("anthropic", api_key="sk-xxx", default=True)
    sent = json.loads(rec.body)
    assert sent == {"api_key": "sk-xxx", "default": True}

    _set(rec, body={"providers": []})
    dj.put_provider("anthropic", api_key="sk-xxx")  # default=False omitted
    assert json.loads(rec.body) == {"api_key": "sk-xxx"}


def test_audit_limit(server):
    rec, dj = server
    _set(rec, body={"entries": [], "total": 0, "returned": 0, "verified": True})
    dj.audit(limit=5)
    assert rec.path == "/v1/audit?limit=5"


def test_audit_filters(server):
    rec, dj = server
    _set(rec, body={"entries": [], "total": 0, "returned": 0, "verified": True})
    dj.audit(island="web", type_prefix="port.", actor="alice", decision="denied")
    # query params are present (order not guaranteed)
    assert "island=web" in rec.path
    assert "type=port." in rec.path
    assert "actor=alice" in rec.path
    assert "decision=denied" in rec.path


def test_audit_csv_returns_text(server):
    rec, dj = server
    rec.status = 200
    rec.resp = b"seq,type\n1,port.grant\n"
    rec.content_type = "text/plain"
    out = dj.audit(format="csv")
    assert out == "seq,type\n1,port.grant\n"
    assert "format=csv" in rec.path


def test_mcp_grant_list_revoke(server):
    rec, dj = server
    _set(rec, body={"grants": []})
    dj.list_mcp_grants("foo")
    assert rec.path == "/v1/islands/foo/mcp/grants"

    _set(rec, status=201, body={"server": "files"})
    dj.grant_mcp("foo", "files")
    assert rec.method == "POST" and json.loads(rec.body) == {"server": "files"}

    _set(rec, status=204)
    dj.revoke_mcp("foo", "files")
    assert rec.method == "DELETE" and rec.path == "/v1/islands/foo/mcp/grants/files"


def test_mcp_call(server):
    rec, dj = server
    _set(rec, body={"ok": True})
    dj.mcp_call("files", "tools/list", params={"x": 1})
    assert rec.path == "/v1/mcp/call"
    assert json.loads(rec.body) == {"server": "files", "method": "tools/list", "params": {"x": 1}}


def test_create_token(server):
    rec, dj = server
    _set(rec, status=201, body={"token": {"id": "t1"}, "secret": "sk-once"})
    out = dj.create_token("operator", label="ci", islands=["web"])
    assert rec.method == "POST" and rec.path == "/v1/tokens"
    assert json.loads(rec.body) == {"role": "operator", "label": "ci", "islands": ["web"]}
    assert out["secret"] == "sk-once"


def test_list_and_revoke_token(server):
    rec, dj = server
    _set(rec, body={"tokens": []})
    dj.list_tokens()
    assert rec.path == "/v1/tokens"

    _set(rec, status=204)
    dj.revoke_token("t1")
    assert rec.method == "DELETE" and rec.path == "/v1/tokens/t1"


def test_activity_filters(server):
    rec, dj = server
    _set(rec, body={"items": [], "returned": 0, "audit_enabled": True})
    out = dj.activity(actor="alice", kind="broker", island="web", limit=10)
    assert rec.path.startswith("/v1/activity?")
    assert "actor=alice" in rec.path
    assert "kind=broker" in rec.path
    assert "island=web" in rec.path
    assert "limit=10" in rec.path
    assert out["audit_enabled"] is True


def test_overview(server):
    rec, dj = server
    _set(rec, body={"total_islands": 3, "running": 2})
    out = dj.overview()
    assert rec.path == "/v1/overview"
    assert out["total_islands"] == 3


def test_healthz_true_false(server):
    rec, dj = server
    _set(rec, status=200, body={"status": "ok"})
    assert dj.healthz() is True
    _set(rec, status=503, body={"error": "down"})
    assert dj.healthz() is False


def test_error_decodes_json_message(server):
    rec, dj = server
    _set(rec, status=409, body={"error": "island has unpushed work"})
    with pytest.raises(DejimaError) as ei:
        dj.delete_island("foo")
    assert ei.value.status == 409
    assert ei.value.message == "island has unpushed work"


def test_error_falls_back_to_text(server):
    rec, dj = server
    rec.status = 500
    rec.resp = b"boom"
    rec.content_type = "text/plain"
    with pytest.raises(DejimaError) as ei:
        dj.list_islands()
    assert ei.value.message == "boom"


def test_session_url_scheme():
    dj = Client(host="h:1", scheme="https")
    assert dj.session_url("foo") == "wss://h:1/v1/islands/foo/session"
    assert dj.session_url("foo", agent="p2") == "wss://h:1/v1/islands/foo/agents/p2/session"
    dj2 = Client(host="h:1")
    assert dj2.session_url("foo").startswith("ws://")


def test_terminal_session_url():
    dj = Client(host="h:1")
    assert dj.terminal_session_url("t1", label="me") == "ws://h:1/v1/terminals/t1/session?label=me"
