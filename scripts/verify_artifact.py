"""Install a built candidate into a temporary application. No registry uploads."""

import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
ARTIFACTS = ROOT / "artifacts"


def run(command, cwd, env=None):
    subprocess.run([str(value) for value in command], cwd=cwd, env=env, check=True)


def unpack(artifact, destination):
    if artifact.suffix == ".zip":
        with zipfile.ZipFile(artifact) as archive:
            for name in archive.namelist():
                if (
                    not (destination / name)
                    .resolve()
                    .is_relative_to(destination.resolve())
                ):
                    raise ValueError("Unsafe archive path")
            archive.extractall(destination)
    else:
        with tarfile.open(artifact) as archive:
            for item in archive.getmembers():
                if (
                    item.issym()
                    or item.islnk()
                    or (
                        not (destination / item.name)
                        .resolve()
                        .is_relative_to(destination.resolve())
                    )
                ):
                    raise ValueError("Unsafe archive entry")
            archive.extractall(destination, filter="data")


with tempfile.TemporaryDirectory(prefix="originchaindb-install-") as temporary:
    app = Path(temporary)
    unpack(
        next(ARTIFACTS.glob("*.crate" if "go" == "rust" else "*.tar.gz")),
        app / "package",
    )
    module = next((app / "package").rglob("go.mod")).parent
    (app / "go.mod").write_text(
        "module installcheck\n\ngo 1.23\n\nrequire github.com/originchaindb/originchaindb-go v0.0.0\nreplace github.com/originchaindb/originchaindb-go => "
        + module.as_posix()
        + "\n"
    )
    (app / "main.go").write_text(
        'package main\nimport ("context"; db "github.com/originchaindb/originchaindb-go")\nfunc main(){c,e:=db.New("https://example.invalid","test");if e!=nil{panic(e)};defer c.Close();_ = c.Rows.Pages(context.Background(),"app.items","id",db.PageOptions{});_ = db.BatchResult{Offset:0,Count:1,Inserted:1}}\n'
    )
    run(["go", "run", "."], app, dict(os.environ, GOPROXY="off"))
print("Fresh package application passed: " + "go")
