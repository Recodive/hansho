import json
import os
import threading
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable, Dict, List, Optional

from hansho import Hansho

TEST_KEY = "ck_test_SECRET_do_not_leak"
SPEC_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "spec")


@dataclass
class Recorded:
    method: str
    #: The raw request target, exactly as sent.
    url: str
    headers: Dict[str, str]
    body: bytes


@dataclass
class Reply:
    status: int = 200
    headers: Dict[str, str] = field(default_factory=dict)
    body: str = "{}"


class Hub:
    """A local stand-in for the hub. `fn` fills the reply (default 200 {})."""

    def __init__(self, fn: Optional[Callable[[Recorded, Reply], None]] = None) -> None:
        self.requests: List[Recorded] = []
        self.waits: List[float] = []
        self.errors: List[BaseException] = []
        hub = self

        class Handler(BaseHTTPRequestHandler):
            def handle_any(self) -> None:
                n = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(n) if n else b""
                rec = Recorded(self.command, self.path, {k.lower(): v for k, v in self.headers.items()}, body)
                hub.requests.append(rec)
                reply = Reply()
                try:
                    if fn:
                        fn(rec, reply)
                except BaseException as e:  # assertion failures inside the handler thread
                    hub.errors.append(e)
                    reply.status = 599
                self.send_response(reply.status)
                self.send_header("Content-Type", "application/json")
                for k, v in reply.headers.items():
                    self.send_header(k, v)
                data = b"" if reply.status == 204 else reply.body.encode("utf-8")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = handle_any

            def log_message(self, *args: Any) -> None:
                pass

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self._server.daemon_threads = True
        self.url = f"http://127.0.0.1:{self._server.server_address[1]}"
        self._thread = threading.Thread(target=self._server.serve_forever, args=(0.02,), daemon=True)
        self._thread.start()
        self.client = self.new_client()

    def new_client(self, **kw: Any) -> Hansho:
        kw.setdefault("sleep", self.waits.append)
        return Hansho(kw.pop("base_url", self.url), TEST_KEY, **kw)

    def close(self) -> None:
        self._server.shutdown()
        self._server.server_close()
        self._thread.join(5)

    def check(self) -> None:
        """Re-raises what went wrong inside the handler thread."""
        if self.errors:
            raise self.errors[0]

    def __enter__(self) -> "Hub":
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()
        if not any(exc):
            self.check()


def load_json(*rel: str) -> Any:
    with open(os.path.join(SPEC_DIR, *rel), encoding="utf-8") as f:
        return json.load(f)
