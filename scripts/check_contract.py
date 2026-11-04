"""Verify the vendored API v1 contract against the pinned organization reference."""
import hashlib
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parent.parent
reference = Path(sys.argv[1]) if len(sys.argv) > 1 else ROOT / ".contract-reference/sdk-contract/v1"
files = {"run.py": "tests/contract/run.py", "resilience.py": "tests/contract/resilience.py", "sdk-contract.json": "sdk-contract.json"}
for name, relative in files.items():
    actual = (ROOT / relative).read_bytes().replace(b"\r\n", b"\n")
    expected = (reference / name).read_bytes().replace(b"\r\n", b"\n")
    if actual != expected:
        raise SystemExit(f"Shared SDK contract differs: {relative}")
manifest = json.loads((ROOT / "sdk-contract.json").read_text())
for name, field in [("run.py", "contract_test_sha256"), ("resilience.py", "resilience_test_sha256")]:
    data = (ROOT / "tests/contract" / name).read_bytes().replace(b"\r\n", b"\n")
    if hashlib.sha256(data).hexdigest() != manifest[field]:
        raise SystemExit(f"Shared SDK fixture digest differs: {name}")
print("API v1 manifest and both HTTP fixtures match the pinned shared contract")
