"""Dejima — Python client for the Dejima API.

    from dejima import Client
    dj = Client()                       # reads DEJIMA_HOST / DEJIMA_TOKEN
    isl = dj.create_island(repo="git@github.com:you/foo.git", agent="claude-code")
    print(dj.list_islands())

See https://aoos.github.io/dejima/api.html for the full API. Dejima is alpha
(0.x): fields may change until 1.0. This client is hand-written over the REST
surface; the WebSocket PTY session (``Client.attach`` → ``Session``) needs the
``ws`` extra: ``pip install 'dejima[ws]'``.
"""

from .client import Client, DejimaError, Session

__all__ = ["Client", "DejimaError", "Session", "__version__"]
__version__ = "0.1.0"
