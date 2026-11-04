"""Verify vendored API v1 contracts against the pinned organization reference."""
import hashlib
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parent.parent
reference = Path(sys.argv[1]) if len(sys.argv) > 1 else ROOT / ".contract-reference/sdk-contract/v1"
FILES = {'run.py': 'tests/contract/run.py', 'resilience.py': 'tests/contract/resilience.py', 'iteration.py': 'tests/contract/iteration.py', 'benchmark.py': 'benchmarks/run.py', 'check_api.py': 'scripts/check_api.py', 'sdk-contract.json': 'sdk-contract.json'}
DIGESTS = {'run.py': 'contract_test_sha256', 'resilience.py': 'resilience_test_sha256', 'iteration.py': 'iteration_test_sha256', 'benchmark.py': 'benchmark_test_sha256', 'check_api.py': 'api_guard_sha256'}
for name, relative in FILES.items():
    actual = (ROOT / relative).read_bytes().replace(b"\r\n", b"\n")
    expected = (reference / name).read_bytes().replace(b"\r\n", b"\n")
    if actual != expected:
        raise SystemExit(f"Shared SDK contract differs: {relative}")
manifest = json.loads((ROOT / "sdk-contract.json").read_text())
for name, field in DIGESTS.items():
    data = (ROOT / FILES[name]).read_bytes().replace(b"\r\n", b"\n")
    if hashlib.sha256(data).hexdigest() != manifest[field]:
        raise SystemExit(f"Shared SDK fixture digest differs: {name}")
print("API v1 manifest, three HTTP fixtures, benchmark and API guard match the pinned contract")
