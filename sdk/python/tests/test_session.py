"""Tests for the PTY Session envelope protocol — using a fake websocket so no
``ws`` extra or live daemon is needed."""

from __future__ import annotations

import base64
import json

import pytest

from dejima import DejimaError
from dejima.client import Session


class FakeWS:
    """A minimal stand-in for websocket._core.WebSocket."""

    def __init__(self, incoming=None):
        self.sent = []
        self._incoming = list(incoming or [])
        self.closed = False

    def send(self, data):
        self.sent.append(data)

    def recv(self):
        if not self._incoming:
            return ""  # closed
        return self._incoming.pop(0)

    def close(self):
        self.closed = True


def test_send_wraps_in_data_envelope():
    ws = FakeWS()
    Session(ws).send(b"ls -la\n")
    env = json.loads(ws.sent[0])
    assert env["type"] == "data"
    assert base64.b64decode(env["b64"]) == b"ls -la\n"


def test_resize_envelope():
    ws = FakeWS()
    Session(ws).resize(40, 120)
    assert json.loads(ws.sent[0]) == {"type": "resize", "rows": 40, "cols": 120}


def test_recv_skips_hello_and_decodes_data():
    hello = json.dumps({"type": "hello", "attached": []})
    data = json.dumps({"type": "data", "b64": base64.b64encode(b"output").decode()})
    s = Session(FakeWS([hello, data]))
    assert s.recv() == b"output"


def test_recv_returns_none_on_close():
    s = Session(FakeWS([]))
    assert s.recv() is None


def test_recv_raises_on_error_envelope():
    err = json.dumps({"type": "error", "b64": "session revoked"})
    s = Session(FakeWS([err]))
    with pytest.raises(DejimaError) as ei:
        s.recv()
    assert "session revoked" in ei.value.message


def test_context_manager_closes():
    ws = FakeWS()
    with Session(ws) as s:
        s.send(b"x")
    assert ws.closed is True
