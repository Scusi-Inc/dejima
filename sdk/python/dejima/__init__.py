"""Dejima — Python client for the Dejima API.

    from dejima import Client
    dj = Client()                       # reads DEJIMA_HOST / DEJIMA_TOKEN
    isl = dj.create_island(repo="git@github.com:you/foo.git", agent="claude-code")
    print(dj.list_islands())

See https://aoos.github.io/dejima/api.html for the full API. Dejima is alpha
(0.x): fields may change until 1.0. This client is hand-written over the REST
surface; the WebSocket PTY session (attach) needs the `ws` extra.
"""

from .client import Client, DejimaError

__all__ = ["Client", "DejimaError", "__version__"]
__version__ = "0.1.0"
