"""Black-box HTTP contract for the vector index record operations.

Written to be shared unchanged by every language SDK, like run.py. It is not
yet part of the pinned organization contract (sdk-contract.json does not list
its digest), so it is run on its own:

    python tests/contract/vector_indexes.py -- <consumer command>

The consumer reads OC_URL and OC_TOKEN, makes the calls listed in EXPECTED
below exactly once each, and checks the answers. No service credentials are
used. A failing or missing consumer check fails the run.
"""

import collections
import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

TOKEN = "contract-only-token"
PREFIX = "/opaque/database%2Froute"
BASE = "/vector-indexes"
INDEX = {
    "index_id": "01J9Z2M4Q5W7Y8X3K6N0P2R4T6",
    "name": "demo",
    "state": "active",
    "dimension": 2,
    "metric": "cosine",
    "score_direction": "higher_is_better",
    "sparse": False,
    "text_fields": [],
    "deletion_protection": "disabled",
    "tags": {},
    "future_field": {"kept": True},
}
RECEIPT = {"position": {"domain": "wal", "value": "1:2"}, "durability_scope": "local_fsync"}


def envelope(code, status, retryable, details=None):
    error = {"code": code, "message": "server detail", "status": status, "retryable": retryable}
    if details is not None:
        error["details"] = details
    return {"error": error}


# (method, path below the database address, request marker) -> count
EXPECTED = {
    ("POST", BASE, "create"): 1,
    ("GET", BASE + "/demo%2Fone", ""): 1,
    ("POST", BASE + "/list", "page-1"): 1,
    ("POST", BASE + "/list", "page-2"): 1,
    ("POST", BASE + "/demo/records/upsert", "ok"): 1,
    ("POST", BASE + "/demo-partial/records/upsert", "batch-1"): 1,
    ("POST", BASE + "/demo-partial/records/upsert", "batch-2"): 1,
    ("POST", BASE + "/demo/search", "dense"): 1,
    ("POST", BASE + "/demo/search", "hybrid"): 1,
    ("POST", BASE + "/demo/records/fetch", ""): 1,
    ("POST", BASE + "/demo/records/delete", "ids"): 1,
    ("POST", BASE + "/demo/records/delete", "filter"): 1,
    ("GET", BASE + "/demo/jobs/01JOB", ""): 1,
    ("POST", BASE + "/demo/namespaces/list", ""): 1,
    ("GET", BASE + "/demo/stats", ""): 1,
    ("POST", BASE + "/demo/records/count", ""): 1,
    ("GET", BASE + "/contract-unauthorized", ""): 1,
    ("GET", BASE + "/contract-key-scope", ""): 1,
    ("GET", BASE + "/contract-missing", ""): 1,
    ("POST", BASE + "/demo/search", "sparse"): 1,
    ("GET", BASE + "/contract-not-enabled", ""): 1,
    ("POST", BASE + "/demo/search", "unavailable"): 1,
    ("GET", BASE + "/contract-redirect", ""): 1,
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
            pass

    def do_GET(self):
        self.handle_call()

    def do_POST(self):
        self.handle_call()

    def do_PATCH(self):
        self.handle_call()

    def do_DELETE(self):
        self.handle_call()

    def handle_call(self):
        try:
            self.route()
        except Exception as error:
            failures.append(str(error))
            self.respond(500, {"error": "contract assertion failed"})

    def respond(self, status, body, headers=None):
        data = json.dumps(body, separators=(",", ":")).encode()
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
        assert self.headers.get("Authorization") == "Bearer " + TOKEN, "incorrect bearer header"
        assert not parsed.query, "record operations carry no query string"
        path = parsed.path[len(PREFIX):]
        data = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.command == "GET":
            assert not data, "a GET carries no body"
            body = None
        else:
            assert self.headers.get("Content-Type", "").startswith("application/json"), (
                "record operation bodies are JSON"
            )
            body = json.loads(data)
        marker = ""
        answer = None

        if path == BASE and self.command == "POST":
            assert body == {"name": "demo", "dimension": 2, "metric": "cosine"}, body
            marker, answer = "create", (201, INDEX)
        elif path == BASE + "/demo%2Fone":
            answer = (200, INDEX)
        elif path == BASE + "/list":
            if body == {"limit": 1}:
                marker, answer = "page-1", (200, {"indexes": [INDEX], "next_cursor": "c1"})
            else:
                assert body == {"limit": 1, "cursor": "c1"}, body
                marker = "page-2"
                answer = (200, {"indexes": [dict(INDEX, name="zeta")]})
        elif path == BASE + "/demo/records/upsert":
            assert body == {
                "namespace": "ns",
                "records": [
                    {"id": "one", "values": [1.0, 0.0], "metadata": {"tag": "test", "n": 1}},
                    {"id": "two", "sparse_values": {"indices": [3], "values": [0.5]}},
                ],
            }, body
            marker = "ok"
            answer = (200, {"upserted_count": 2, "namespace_created": True, "receipt": RECEIPT})
        elif path == BASE + "/demo-partial/records/upsert":
            ids = [r["id"] for r in body["records"]]
            if ids == ["p0", "p1"]:
                marker, answer = "batch-1", (200, {"upserted_count": 2, "receipt": RECEIPT})
            else:
                assert ids == ["p2", "p3"], ids
                marker = "batch-2"
                answer = (409, envelope(
                    "configuration_limit_exceeded", 409, False,
                    {"reason": "exact_index_capacity", "limit": 2},
                ))
        elif path == BASE + "/demo/search":
            if "sparse" in body and "dense" not in body:
                assert body == {
                    "namespace": "ns", "mode": "semantic", "k": 1,
                    "sparse": {"indices": [3], "values": [0.5]},
                }, body
                marker = "sparse"
                answer = (409, envelope(
                    "index_capability_missing", 409, False, {"reason": "sparse_not_declared"},
                ))
            elif body.get("k") == 3:
                assert body == {
                    "namespace": "ns", "mode": "semantic", "k": 3, "dense": {"vector": [1.0, 0.0]},
                }, body
                marker = "unavailable"
                answer = (503, envelope("index_rebuilding", 503, True), {"Retry-After": "0"})
            elif body.get("mode") == "hybrid":
                assert body == {
                    "namespace": "ns", "mode": "hybrid", "k": 2,
                    "dense": {"vector": [1.0, 0.0]},
                    "lexical": {"field": "body", "query": "hello"},
                }, body
                marker = "hybrid"
                answer = (200, {"mode": "hybrid", "ranking_method": "rrf", "returned": 0, "hits": []})
            else:
                assert body == {
                    "namespace": "ns", "mode": "semantic", "k": 2,
                    "dense": {"vector": [1.0, 0.0], "quality": "exact"},
                    "filter_expr": {"tag": {"$eq": "test"}},
                    "include_metadata": True,
                }, body
                marker = "dense"
                answer = (200, {
                    "namespace": "ns", "mode": "semantic", "ranking_method": "dense_similarity",
                    "k": 2, "returned": 2, "completeness": {"value": "exact"},
                    "hits": [
                        {"id": "one", "score": 1.0, "rank": 1, "metadata": {"tag": "test", "n": 1}},
                        {"id": "two", "score": 0.5, "rank": 2, "future": True},
                    ],
                    "future_field": True,
                })
        elif path == BASE + "/demo/records/fetch":
            assert body == {
                "namespace": "ns", "ids": ["one", "missing"], "include_values": True,
            }, body
            answer = (200, {
                "records": {"one": {"id": "one", "values": [1.0, 0.0], "rev": 1}},
                "missing": ["missing"], "namespace": "ns",
            })
        elif path == BASE + "/demo/records/delete":
            if "ids" in body:
                assert body == {"namespace": "ns", "ids": ["one"]}, body
                marker = "ids"
                answer = (200, {"deleted": 1, "missing": 0, "receipt": RECEIPT})
            else:
                assert body == {"namespace": "ns", "filter_expr": {"tag": {"$in": ["a", "b"]}}}, body
                marker, answer = "filter", (202, {"job_id": "01JOB", "receipt": RECEIPT})
        elif path == BASE + "/demo/jobs/01JOB":
            answer = (200, {"job_id": "01JOB", "kind": "delete_by_filter", "state": "succeeded",
                            "progress": {"examined": 2, "matched": 1, "deleted": 1}})
        elif path == BASE + "/demo/namespaces/list":
            assert body == {}, body
            answer = (200, {"namespaces": [{"name": "ns", "record_count": 1}]})
        elif path == BASE + "/demo/stats":
            answer = (200, {"total_record_count": 1, "namespaces": [], "truncated": False})
        elif path == BASE + "/demo/records/count":
            assert body == {"namespace": "ns", "filter": {"tag": "test"}}, body
            answer = (200, {"count": 1, "namespace": "ns"})
        elif path == BASE + "/contract-unauthorized":
            answer = (401, envelope("unauthorized", 401, False))
        elif path == BASE + "/contract-key-scope":
            answer = (403, envelope("key_scope_insufficient", 403, False))
        elif path == BASE + "/contract-missing":
            answer = (404, envelope("index_not_found", 404, False))
        elif path == BASE + "/contract-not-enabled":
            answer = (501, envelope(
                "not_implemented", 501, False, {"reason": "record_api_not_enabled"},
            ))
        elif path == BASE + "/contract-redirect":
            answer = (307, {}, {"Location": PREFIX + "/redirect-target"})
        elif path == "/redirect-target":
            raise AssertionError("SDK followed a redirect")
        else:
            raise AssertionError("unexpected path " + path)
        calls[(self.command, path, marker)] += 1
        self.respond(*answer)


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
    if dict(calls) != EXPECTED:
        missing = {k: v for k, v in EXPECTED.items() if calls.get(k) != v}
        extra = {k: v for k, v in calls.items() if k not in EXPECTED}
        failures.append(f"Expected exactly one execution of each operation: "
                        f"wrong or missing {missing}, unexpected {extra}")
    if completed.returncode or failures:
        for failure in failures:
            print("FAIL:", failure, file=sys.stderr)
        raise SystemExit(completed.returncode or 1)
    print(
        f"Vector index HTTP contract passed: {sum(EXPECTED.values())} requests; opaque URL, "
        "JSON bodies, acknowledged batch ranges, error envelopes, no retries, no redirects."
    )
