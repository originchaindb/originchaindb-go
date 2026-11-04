"""Record hashes of locally verified build candidates; never publishes."""
import hashlib
import json
import os
from pathlib import Path

root = Path("artifacts")
files = sorted(p for p in root.iterdir() if p.is_file() and p.name != "build.json")
if not files:
    raise SystemExit("No verified build artifacts")
manifest = {"source_repository": os.environ["GITHUB_REPOSITORY"], "source_commit": os.environ["GITHUB_SHA"],
            "workflow_run": os.environ["GITHUB_RUN_ID"], "registry_published": False,
            "artifacts": [{"name": p.name, "sha256": hashlib.sha256(p.read_bytes()).hexdigest(), "bytes": p.stat().st_size} for p in files]}
(root / "build.json").write_text(json.dumps(manifest, indent=2) + "\n")
