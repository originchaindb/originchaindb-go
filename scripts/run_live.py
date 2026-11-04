"""Run an explicit live check; no silent skips and no credential output."""
import json
import os
import subprocess
import sys

for name in ("OC_URL", "OC_TOKEN", "OC_DEPLOYMENT"):
    if not os.environ.get(name):
        raise SystemExit(f"Missing {name}; configure the sdk-integration environment")
if len(sys.argv) < 3 or sys.argv[1] != "--":
    raise SystemExit("Usage: run_live.py -- command [arguments]")
try:
    result = subprocess.run(sys.argv[2:], capture_output=True, text=True, timeout=300)
except subprocess.TimeoutExpired:
    raise SystemExit("Live integration timed out") from None
if result.returncode or "Widget" not in result.stdout or "p1" not in result.stdout:
    raise SystemExit("Live integration failed; reproduce the documented example locally")
print(json.dumps({"passed": True, "commit": os.environ.get("GITHUB_SHA"),
                  "deployment": os.environ["OC_DEPLOYMENT"],
                  "coverage": "read-only bound SQL, token provider, typed rows, local observer"}))
