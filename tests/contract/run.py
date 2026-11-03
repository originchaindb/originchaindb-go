"""Black-box HTTP contract test, shared unchanged by every language SDK.

Run from a repository root: python tests/contract/run.py -- <consumer command>
No service credentials are used. A failing/missing consumer check fails the run.
"""

import collections
import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

TOKEN = "contract-only-token"
PREFIX = "/opaque/database%2Froute"
SELECT = {
    "kind": "select",
    "rows": [
        {"id": 9007199254740993, "amount": "1.00000000000000001", "nothing": None}
    ],
    "columns": ["id", "amount", "nothing"],
    "column_types": {"id": "bigint", "amount": "numeric"},
    "future_field": {"kept": True},
}
BATCH = {
    "kind": "batch",
    "results": [
        {"kind": "insert", "inserted": 1},
        {"kind": "batch", "results": [{"error": "duplicate key", "sqlstate": "23505"}]},
    ],
}
calls = collections.Counter()
failures = []


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def handle(self):
        try:
            super().handle()
        except (ConnectionResetError, BrokenPipeError):
            pass  # A consumer process can close an idle keep-alive socket.

    def do_GET(self):
        self.handle_call()

    def do_POST(self):
        self.handle_call()

    def do_DELETE(self):
        self.handle_call()

    def handle_call(self):
        try:
            self.route()
        except Exception as error:
            failures.append(str(error))
            self.respond(500, {"error": "contract assertion failed"})

    def respond(self, status, body, headers=None, raw=False):
        data = (
            body.encode() if raw else json.dumps(body, separators=(",", ":")).encode()
        )
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("X-OC-Request-Id", "84f92822-5837-432e-981a-7b24ad65e16c")
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(data)

    def route(self):
        parsed = urlsplit(self.path)
        assert parsed.path.startswith(PREFIX + "/"), "opaque database URL changed"
        assert self.headers.get("Authorization") == "Bearer " + TOKEN, (
            "incorrect bearer header"
        )
        path = parsed.path[len(PREFIX) :]
        query = parse_qs(parsed.query)
        data = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        body = (
            json.loads(data)
            if data and "application/json" in self.headers.get("Content-Type", "")
            else data.decode()
        )
        if path == "/sql":
            assert self.command == "POST", "SQL must use POST"
            name = body["sql"]
            calls[name] += 1
            assert isinstance(body.get("params", []), list), (
                "SQL parameters must be positional"
            )
            if name == "contract:select":
                assert body["params"] == [7, "quote' / 雪", None], (
                    "SQL values changed or interpolated"
                )
                self.respond(200, SELECT)
            elif name == "contract:update":
                self.respond(
                    200,
                    {
                        "kind": "update",
                        "rows_affected": 2,
                        "rows_buffered": 3,
                        "returning": ["id"],
                        "rows": [{"id": 1}],
                        "future_field": True,
                    },
                )
            elif name == "contract:unknown":
                self.respond(200, {"kind": "future_statement", "future_field": True})
            elif name == "contract:batch":
                self.respond(200, BATCH)
            elif name == "contract:unavailable":
                self.respond(
                    503,
                    {"error": {"code": "busy", "message": "server detail"}},
                    {"Retry-After": "0"},
                )
            elif name == "contract:unauthorized":
                self.respond(
                    401, {"error": {"code": "unauthorized", "message": "access denied"}}
                )
            elif name == "contract:redirect":
                self.respond(307, {}, {"Location": PREFIX + "/redirect-target"})
            elif name == "contract:malformed":
                self.respond(200, "{invalid", raw=True)
            elif name == "contract:invalid-rows":
                self.respond(200, {"kind": "select", "rows": "invalid"})
            else:
                raise AssertionError("unexpected SQL fixture " + name)
            return
        calls[path] += 1
        if path == "/redirect-target":
            raise AssertionError("SDK followed a redirect")
        if path == "/rows/demo%2Fitems/a%20b%3F%2F%25":
            assert self.command == "GET"
            self.respond(200, {"id": "one"})
        elif path == "/rows/demo.items":
            assert self.command == "POST" and body == {"id": "one", "nullable": None}
            self.respond(200, {"inserted": 1})
        elif path == "/rows/demo.items/_batch":
            assert body == [{"id": "one"}, {"id": "two"}]
            self.respond(200, {"inserted": 2})
        elif path == "/schemas":
            if self.command == "POST":
                assert body == '[schema]\nname="demo.items"\n', "TOML body changed"
                assert self.headers["Content-Type"].startswith("text/plain")
                self.respond(200, {"id": "demo.items", "tenant": "fixture"})
            else:
                self.respond(200, ["demo.items"])
        elif path == "/vector/demo.items/put":
            assert body == {
                "id": "one",
                "embedding": [1.0, 0.0],
                "dim": 2,
                "metadata": {"tag": "test"},
            }
            self.respond(200, {"ok": True})
        elif path == "/vector/demo.items/topk":
            assert body == {"query": [1.0, 0.0], "dim": 2, "k": 10, "metric": "cosine"}
            self.respond(
                200,
                [
                    {"id": "one", "score": 1.0, "metadata": {"tag": "test"}},
                    {"vec_id": "two", "score": 0.5, "future": True},
                ],
            )
        elif path == "/vector/demo.items/one":
            assert self.command == "DELETE"
            self.respond(
                200,
                {"deleted": True, "id": "one", "table": "demo.items", "index": "hnsw"},
            )
        elif path == "/fts/demo.items/body":
            if self.command == "POST":
                assert body == {"doc_id": "one", "text": "hello world"}
                self.respond(200, {"ok": True})
            elif query.get("q") == ["phrase"]:
                assert query == {"q": ["phrase"], "mode": ["phrase"]}
                self.respond(200, ["two"])
            elif query.get("q") == ["highlight"]:
                assert query == {
                    "q": ["highlight"],
                    "mode": ["bm25"],
                    "k": ["10"],
                    "highlight": ["true"],
                    "facets": ["tag"],
                }
                self.respond(
                    200,
                    {
                        "hits": [
                            {
                                "doc_id": "one",
                                "score": 1.0,
                                "highlights": {"body": ["<em>hello</em>"]},
                            }
                        ],
                        "facets": {"tag": [{"value": "test", "count": 1}]},
                    },
                )
            else:
                assert query == {"q": ["hello"], "mode": ["bm25"], "k": ["10"]}
                self.respond(200, [{"doc_id": "one", "score": 1.0}])
        elif path == "/cypher":
            assert body == {
                "cypher": "MATCH (n) RETURN $name",
                "params": {"name": "one"},
            }
            self.respond(200, {"kind": "select", "rows": [{"name": "one"}]})
        elif path == "/graph/demo.items/neighbors":
            assert query == {"pk": ["one"], "rel": ["related"]}
            self.respond(200, ["two"])
        elif path == "/graph/demo.items/bfs":
            assert query == {"pk": ["one"], "rel": ["related"], "max_depth": ["5"]}
            self.respond(200, [{"pk": "two", "depth": 1}])
        else:
            raise AssertionError("unexpected path " + path)


if __name__ == "__main__":
    command = sys.argv[1:]
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        raise SystemExit("Provide a consumer command after --")
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    env = dict(
        os.environ,
        OC_URL=f"http://127.0.0.1:{server.server_port}{PREFIX}",
        OC_TOKEN=TOKEN,
        OC_CONTRACT="1",
    )
    completed = subprocess.run(command, env=env)
    server.shutdown()
    expected = {
        f"contract:{name}": 1
        for name in [
            "select",
            "update",
            "unknown",
            "batch",
            "unavailable",
            "unauthorized",
            "redirect",
            "malformed",
            "invalid-rows",
        ]
    }
    expected.update(
        {
            path: 1
            for path in [
                "/rows/demo%2Fitems/a%20b%3F%2F%25",
                "/rows/demo.items",
                "/rows/demo.items/_batch",
                "/vector/demo.items/put",
                "/vector/demo.items/topk",
                "/vector/demo.items/one",
                "/cypher",
                "/graph/demo.items/neighbors",
                "/graph/demo.items/bfs",
            ]
        }
    )
    expected.update({"/schemas": 2, "/fts/demo.items/body": 4})
    if dict(calls) != expected:
        failures.append(
            f"Expected exactly one execution of each operation: {dict(calls)}"
        )
    if completed.returncode or failures:
        for failure in failures:
            print("FAIL:", failure, file=sys.stderr)
        raise SystemExit(completed.returncode or 1)
    print(
        f"HTTP contract passed: {sum(expected.values())} requests; opaque URL, parameters, numeric values, partial failure, no retries, no redirects."
    )
