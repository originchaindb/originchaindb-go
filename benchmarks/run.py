"""Loopback SDK overhead benchmark. Never targets a real database or publishes.

Requires psutil. Compile the consumer before running; startup is excluded from
latency samples, but included in peak resident memory. Three fresh-process runs
reduce timing noise. Latency gates require a reviewed baseline from the same CI
platform; deterministic transport and workload gates always apply.
"""

import argparse
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import platform
import socket
import statistics
import subprocess
import threading
import time

import psutil

parser = argparse.ArgumentParser()
parser.add_argument("--output", default="benchmark-results.json")
parser.add_argument("--baseline")
parser.add_argument(
    "--platform", default=platform.system().lower() + "-" + platform.machine().lower()
)
parser.add_argument("--php-workers", action="store_true")
parser.add_argument("command", nargs=argparse.REMAINDER)
args = parser.parse_args()
command = args.command[1:] if args.command[:1] == ["--"] else args.command
if not command:
    parser.error("pass a prebuilt native consumer after --")

LARGE = json.dumps(
    {"kind": "select", "rows": [{"id": n, "value": "x" * 512} for n in range(2048)]},
    separators=(",", ":"),
).encode()
SMALL = b'{"kind":"select","rows":[{"value":1}]}'


def percentile(values, fraction):
    return sorted(values)[min(len(values) - 1, int((len(values) - 1) * fraction))]


def once():
    counts, connections, errors = defaultdict(int), defaultdict(set), []
    lock = threading.Lock()
    active = peak_active = 0

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_):
            pass

        def setup(self):
            super().setup()
            self.connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

        def handle(self):
            try:
                super().handle()
            except (ConnectionResetError, ConnectionAbortedError):
                pass  # A client may close an idle pooled connection on exit.

        def do_POST(self):
            nonlocal active, peak_active
            try:
                body = json.loads(
                    self.rfile.read(int(self.headers.get("Content-Length", "0")))
                )
                mode = body["sql"].removeprefix("bench:")
                assert mode in ("warm", "sequential", "concurrent", "large")
                assert self.path == "/benchmark/opaque%2Fdatabase/sql"
                assert self.headers.get("Authorization") == "Bearer benchmark-static"
                with lock:
                    counts[mode] += 1
                    connections[mode].add(self.client_address)
                    active += 1
                    peak_active = max(peak_active, active)
                if mode == "concurrent":
                    time.sleep(0.005)
                raw = LARGE if mode == "large" else SMALL
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
                self.wfile.flush()
                with lock:
                    active -= 1
            except Exception as error:
                with lock:
                    errors.append(type(error).__name__)
                self.close_connection = True

    class Server(ThreadingHTTPServer):
        request_queue_size = 128

    server = Server(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    env = {key: value for key, value in os.environ.items() if not key.startswith("OC_")}
    env.update(
        OC_URL=f"http://127.0.0.1:{server.server_port}/benchmark/opaque%2Fdatabase",
        OC_TOKEN="benchmark-static",
    )
    peak_rss = 0

    def execute(worker=False):
        nonlocal peak_rss
        local = dict(env, OC_BENCH_WORKER="1") if worker else env
        with subprocess.Popen(
            command,
            env=local,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
        ) as process:
            tracked = psutil.Process(process.pid)
            deadline = time.monotonic() + 120
            while process.poll() is None:
                try:
                    rss = tracked.memory_info().rss + sum(
                        child.memory_info().rss
                        for child in tracked.children(recursive=True)
                        if child.is_running()
                    )
                    with lock:
                        peak_rss = max(peak_rss, rss)
                except psutil.Error:
                    pass
                if time.monotonic() > deadline:
                    process.kill()
                    raise AssertionError("benchmark consumer exceeded 120 seconds")
                time.sleep(0.01)
            stdout, stderr = process.communicate()
            if process.returncode:
                raise AssertionError(
                    f"consumer failed ({process.returncode}): {stderr[-2000:]}"
                )
            records = [
                json.loads(line.removeprefix("OC_BENCHMARK "))
                for line in stdout.splitlines()
                if line.startswith("OC_BENCHMARK ")
            ]
            assert len(records) == 1, "consumer must emit exactly one benchmark record"
            return records[0]

    try:
        samples = execute()
        if args.php_workers:
            start = time.perf_counter()
            with ThreadPoolExecutor(max_workers=8) as pool:
                workers = list(pool.map(lambda _: execute(True), range(8)))
            samples["concurrent_ms"] = [
                value for worker in workers for value in worker["concurrent_ms"]
            ]
            samples["concurrent_rps"] = 64 / (time.perf_counter() - start)
        assert not errors, errors
        assert dict(counts) == {
            "warm": 10,
            "sequential": 100,
            "concurrent": 64,
            "large": 10,
        }, dict(counts)
        assert len(connections["sequential"]) <= 2, (
            "warm sequential requests did not reuse connections"
        )
        warmed = connections["warm"] | connections["sequential"] | connections["concurrent"]
        assert len(connections["large"] - warmed) <= 1, (
            "large responses repeatedly opened connections after pool warmup"
        )
        assert peak_active >= 2, "concurrent workload was serialized"
        for name, count in [("sequential", 100), ("concurrent", 64), ("large", 10)]:
            values = samples[name + "_ms"]
            assert len(values) == count and all(
                isinstance(v, (float, int)) and 0 <= v < 120000 for v in values
            )
        return {
            "samples": samples,
            "peak_rss_bytes": peak_rss,
            "connections": {k: len(v) for k, v in connections.items()},
            "peak_active_requests": peak_active,
        }
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=3)


runs = [once() for _ in range(3)]
metrics = {}
for mode in ("sequential", "concurrent", "large"):
    for name, fraction in [("p50", 0.5), ("p95", 0.95), ("p99", 0.99)]:
        metrics[f"{mode}_{name}_ms"] = statistics.median(
            percentile(run["samples"][mode + "_ms"], fraction) for run in runs
        )
    metrics[f"{mode}_requests_per_second"] = statistics.median(
        run["samples"].get(mode + "_rps", 0) for run in runs
    )
metrics["peak_rss_bytes"] = statistics.median(run["peak_rss_bytes"] for run in runs)
result = {
    "schema_version": 1,
    "platform": args.platform,
    "metrics": metrics,
    "runs": runs,
    "concurrency_model": "8 PHP processes"
    if args.php_workers
    else "native concurrency (8 workers)",
    "transport_checks_passed": True,
    "baseline_compared": False,
    "regressions": [],
}
if args.baseline and Path(args.baseline).exists():
    baseline = json.loads(Path(args.baseline).read_text())
    if baseline["platform"] == args.platform:
        result["baseline_compared"] = True
        for mode in ("sequential", "concurrent", "large"):
            key = mode + "_p95_ms"
            old, new = baseline["metrics"][key], metrics[key]
            if new > max(old * 2, old + 10):
                result["regressions"].append(key)
        key = "peak_rss_bytes"
        old, new = baseline["metrics"][key], metrics[key]
        if new > max(old * 1.5, old + 32 * 1024 * 1024):
            result["regressions"].append(key)
Path(args.output).write_text(json.dumps(result, indent=2) + "\n")
print(
    json.dumps(
        {
            key: result[key]
            for key in (
                "metrics",
                "transport_checks_passed",
                "baseline_compared",
                "regressions",
            )
        },
        indent=2,
    )
)
raise SystemExit(bool(result["regressions"]))
