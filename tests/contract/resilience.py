"""Shared SDK failure/rotation fixture. Uses local synthetic credentials only.

Run: python tests/contract/resilience.py -- <native consumer command>
Every expected request must occur exactly once. A skipped consumer is a failure.
This models server failures; it does not certify deployed token lifecycle policy.
"""
import collections
import json
import os
import socket
import subprocess
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PREFIX = "/resilience/opaque%2Fdatabase"
TOKEN = "resilience-static"
ROW = {"id": 9007199254740993, "amount": "1.00000000000000001"}
calls = collections.Counter()
failures = []
lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def handle(self):
        try:
            super().handle()
        except ConnectionError:
            pass

    def reply(self, status, body, name, headers=None):
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("X-OC-Request-Id", "84f92822-5837-432e-981a-7b24ad65e16c")
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        try:
            assert self.path == PREFIX + "/sql", "database address changed"
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            name = body["sql"].removeprefix("fault:")
            with lock:
                calls[name] += 1
                count = calls[name]
            token = f"rotate-{count}" if name == "rotate" else TOKEN
            assert self.headers.get("Authorization") == "Bearer " + token, "incorrect credential"
            status = {"expired": 401, "revoked": 401, "forbidden": 403, "limited": 429}.get(name)
            if status:
                self.reply(status, {"error": {"code": "token_" + name, "message": "private server detail"}}, name,
                           {"Retry-After": "2"} if status == 429 else None)
            elif name == "interrupted":
                # Model a committed write whose response was lost. A replay is a failure.
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", "1000")
                self.end_headers()
                self.wfile.write(b'{"kind":"insert"')
                self.wfile.flush()
                self.close_connection = True
                self.connection.shutdown(socket.SHUT_RDWR)
                self.connection.close()
            elif name == "timeout":
                time.sleep(0.4)
                self.reply(200, {"kind": "select", "rows": []}, name)
            elif name == "rotate":
                if count == 1:
                    self.reply(401, {"error": {"code": "token_expired"}}, name)
                else:
                    self.reply(200, {"kind": "select", "rows": [ROW]}, name)
            elif name == "observer":
                self.reply(200, {"kind": "select", "rows": [ROW]}, name)
            else:
                raise AssertionError("unexpected operation " + name)
        except ConnectionError:
            pass
        except Exception as error:
            failures.append(str(error))
            self.reply(500, {"error": "fixture failed"}, "fixture")


if __name__ == "__main__":
    command = sys.argv[1:]
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        raise SystemExit("Provide a native consumer command after --")
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    env = dict(os.environ, OC_URL=f"http://127.0.0.1:{server.server_port}{PREFIX}", OC_TOKEN=TOKEN, OC_RESILIENCE="1")
    env.pop("OC_CONTRACT", None)
    try:
        completed = subprocess.run(command, env=env, timeout=180)
    finally:
        server.shutdown()
        server.server_close()
    expected = {name: 1 for name in ["expired", "revoked", "forbidden", "limited", "interrupted", "timeout", "observer"]}
    expected["rotate"] = 2
    if dict(calls) != expected:
        failures.append(f"Unexpected request counts (replay or skipped check): {dict(calls)}")
    if completed.returncode or failures:
        for failure in failures:
            print("FAIL:", failure, file=sys.stderr)
        raise SystemExit(completed.returncode or 1)
    print("Resilience contract passed: 9 requests; auth, permissions, throttling, interrupted write, timeout, rotation, observers and typed mapping.")
