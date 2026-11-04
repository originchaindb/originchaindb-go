"""Conservative public API guard. Native extractors emit a JSON name/signature map.

Existing names and signatures must remain unchanged. Additions are accepted.
An intentional compatible signature expansion still requires explicit baseline
review. Behavioral compatibility is checked separately by the wire contracts.
"""

import argparse
import json
from pathlib import Path
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("--baseline", default="api/baseline.json")
parser.add_argument(
    "--update",
    action="store_true",
    help="write a baseline for maintainer review; never used by CI",
)
parser.add_argument("command", nargs=argparse.REMAINDER)
args = parser.parse_args()
command = args.command[1:] if args.command[:1] == ["--"] else args.command
if not command:
    parser.error("pass a native surface extractor after --")
result = subprocess.run(
    command, text=True, encoding="utf-8", capture_output=True, check=True
)
surface = json.loads(result.stdout)
if (
    not isinstance(surface, dict)
    or not surface
    or not all(isinstance(k, str) and isinstance(v, str) for k, v in surface.items())
):
    raise SystemExit("Native API extractor returned an invalid surface")
baseline = Path(args.baseline)
if args.update:
    baseline.parent.mkdir(parents=True, exist_ok=True)
    baseline.write_text(
        json.dumps(surface, indent=2, sort_keys=True, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )
    print(f"Wrote {len(surface)} public API entries for review")
else:
    expected = json.loads(baseline.read_text(encoding="utf-8"))
    removed = sorted(expected.keys() - surface.keys())
    changed = sorted(
        name
        for name in expected.keys() & surface.keys()
        if expected[name] != surface[name]
    )
    if removed or changed:
        for name in removed:
            print(f"REMOVED {name}")
        for name in changed:
            print(
                f"CHANGED {name}\n  before: {expected[name]}\n  after:  {surface[name]}"
            )
        raise SystemExit(
            "Public API compatibility requires review; do not regenerate the baseline to hide a breaking change"
        )
    print(
        f"Public API compatible: {len(expected)} baseline entries; {len(surface.keys() - expected.keys())} additions"
    )
