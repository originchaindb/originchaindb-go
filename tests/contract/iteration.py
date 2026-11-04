"""Shared bounded-batch/keyset contract. Synthetic loopback service, not an engine."""
import collections
import json
import os
import re
import socket
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PREFIX = "/iteration/opaque%2Fdatabase"
EXPECTED = {"bulk.ok": 3, "bulk.fail": 2, "bulk.lost": 1, "bulk.unknown": 1,
            "bulk.bytes": 3, "bulk.stop": 1, "scan.items": 3, "scan.stop": 1,
            "scan.resume": 2, "scan.bad": 1, "scan.numeric": 1, "scan.oversize": 1, "scan.empty": 1}
calls = collections.Counter()
failures = []
lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def setup(self):
        super().setup()
        self.connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

    def log_message(self, *args):
        pass

    def handle(self):
        try:
            super().handle()
        except ConnectionError:
            pass

    def reply(self, status, body):
        raw = json.dumps(body, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        try:
            assert self.headers.get("Authorization") == "Bearer iteration-static"
            raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            body = json.loads(raw)
            if self.path.startswith(PREFIX + "/rows/"):
                name = self.path.removeprefix(PREFIX + "/rows/").removesuffix("/_batch")
                assert name in EXPECTED and name.startswith("bulk."), "unexpected write"
                with lock:
                    calls[name] += 1
                    number = calls[name]
                assert number <= EXPECTED[name], "write replayed or prefetched"
                assert isinstance(body, list) and body and all(isinstance(x, dict) for x in body)
                if name == "bulk.bytes":
                    assert len(raw) <= 64 and len(body) == 1
                    assert body[0]["id"] == "abc"[number - 1]
                else:
                    expected_ids = list("abcde"[(number - 1) * 2:number * 2])
                    assert [row["id"] for row in body] == expected_ids
                if name == "bulk.fail" and number == 2:
                    self.reply(503, {"code": "unavailable"})
                elif name == "bulk.lost":
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", "1000")
                    self.end_headers()
                    self.wfile.write(b'{"inserted":')
                    self.wfile.flush()
                    self.close_connection = True
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                else:
                    self.reply(200, {"inserted": None if name == "bulk.unknown" else len(body)})
            else:
                assert self.path == PREFIX + "/sql", "opaque URL changed"
                match = re.fullmatch(r'SELECT \* FROM "scan"\."([a-z]+)"(?: WHERE "id" > \$1)? ORDER BY "id" ASC LIMIT 2', body["sql"])
                assert match, "unexpected keyset SQL"
                name = "scan." + match[1]
                with lock:
                    calls[name] += 1
                    number = calls[name]
                assert name in EXPECTED and number <= EXPECTED[name], "read prefetched or repeated"
                after = {1: None, 2: "b", 3: "d"}[number]
                if name == "scan.resume":
                    after = {1: "b", 2: "d"}[number]
                assert body.get("params") == ([] if after is None else [after]), "continuation was not bound"
                assert (' WHERE ' in body["sql"]) == (after is not None)
                ids = [key for key in "abcde" if after is None or key > after][:2]
                if name == "scan.bad":
                    ids = ["b", "a"]
                if name == "scan.numeric":
                    ids = [42]
                if name == "scan.oversize":
                    ids = ["a", "b", "c"]
                if name == "scan.empty":
                    ids = []
                self.reply(200, {"kind": "select", "rows": [{"id": key, "value": "sample"} for key in ids]})
        except ConnectionError:
            pass
        except Exception as error:
            with lock:
                failures.append(type(error).__name__ + ": " + str(error))
            self.reply(400, {"code": "fixture_mismatch"})


if __name__ == "__main__":
    assert len(sys.argv) > 2 and sys.argv[1] == "--"
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    env = {key: value for key, value in os.environ.items() if not key.startswith("OC_")}
    env.update(OC_URL=f"http://127.0.0.1:{server.server_port}" + PREFIX, OC_TOKEN="iteration-static", OC_ITERATION="1")
    try:
        result = subprocess.run(sys.argv[2:], env=env, timeout=180)
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
    if failures or dict(calls) != EXPECTED or result.returncode:
        print("FAIL:", failures, dict(calls), file=sys.stderr)
        raise SystemExit(1)
    print(f"Iteration contract passed: {sum(EXPECTED.values())} requests; bounded writes, explicit failures, lazy keyset reads and early stop.")
