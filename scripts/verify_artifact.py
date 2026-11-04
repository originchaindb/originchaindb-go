"""Install a built candidate into a temporary application. No registry uploads."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import zipfile
import xml.etree.ElementTree as ET

LANGUAGE = "go"
ROOT = Path(__file__).resolve().parents[1]
ARTIFACTS = ROOT / "artifacts"


def run(command, cwd, env=None):
    subprocess.run([str(value) for value in command], cwd=cwd, env=env, check=True)


def unpack(artifact, destination):
    if artifact.suffix == ".zip":
        with zipfile.ZipFile(artifact) as archive:
            for name in archive.namelist():
                if not (destination / name).resolve().is_relative_to(destination.resolve()):
                    raise ValueError("Unsafe archive path")
            archive.extractall(destination)
    else:
        with tarfile.open(artifact) as archive:
            for item in archive.getmembers():
                if item.issym() or item.islnk() or not (destination / item.name).resolve().is_relative_to(destination.resolve()):
                    raise ValueError("Unsafe archive entry")
            archive.extractall(destination, filter="data")


with tempfile.TemporaryDirectory(prefix="originchaindb-install-") as temporary:
    app = Path(temporary)
    if LANGUAGE == "typescript":
        artifact = next(ARTIFACTS.glob("*.tgz"))
        (app / "package.json").write_text(json.dumps({"private": True}))
        npm = shutil.which("npm.cmd" if os.name == "nt" else "npm") or "npm"
        run([npm, "install", "--ignore-scripts", "--no-audit", "--no-fund", artifact], app)
        run(["node", "-e", "const {Client,BatchWriteError}=require('@originchain/sdk');const db=new Client({url:'https://example.invalid',token:'test'});for(const name of ['writeBatches','pages','scan'])if(typeof db.rows[name]!=='function')throw Error(name);if(!BatchWriteError)throw Error('missing error type');"], app)
    elif LANGUAGE in ("go", "rust"):
        unpack(next(ARTIFACTS.glob("*.crate" if LANGUAGE == "rust" else "*.tar.gz")), app / "package")
        if LANGUAGE == "go":
            module = next((app / "package").rglob("go.mod")).parent
            (app / "go.mod").write_text('module installcheck\n\ngo 1.23\n\nrequire github.com/originchaindb/originchaindb-go v0.0.0\nreplace github.com/originchaindb/originchaindb-go => ' + module.as_posix() + '\n')
            (app / "main.go").write_text('package main\nimport ("context"; db "github.com/originchaindb/originchaindb-go")\nfunc main(){c,e:=db.New("https://example.invalid","test");if e!=nil{panic(e)};defer c.Close();_ = c.Rows.Pages(context.Background(),"app.items","id",db.PageOptions{});_ = db.BatchResult{Offset:0,Count:1,Inserted:1}}\n')
            run(["go", "run", "."], app, dict(os.environ, GOPROXY="off"))
        else:
            module = next((app / "package").rglob("Cargo.toml")).parent
            (app / "Cargo.toml").write_text('[package]\nname="installcheck"\nversion="0.0.0"\nedition="2021"\n[dependencies]\noriginchaindb={path=' + json.dumps(module.as_posix()) + '}\n')
            (app / "src").mkdir()
            (app / "src/main.rs").write_text('use originchaindb::{Client,BatchOptions,PageOptions};\nfn main(){let db=Client::new("https://example.invalid","test").unwrap();let rows=db.rows();let _b=rows.write_batches("app.items",vec![],BatchOptions::default());let _p=rows.pages("app.items","id",PageOptions::default());}\n')
            run(["cargo", "check", "--offline"], app, dict(os.environ, CARGO_TARGET_DIR=str(Path(os.environ.get("CARGO_TARGET_DIR", str(ROOT / "target"))).resolve())))
    elif LANGUAGE == "java":
        artifact = next(path for path in ARTIFACTS.glob("*.jar") if not path.name.endswith(("-sources.jar", "-javadoc.jar")))
        cp = str(artifact) + os.pathsep + str(ROOT / "target/dependency/*")
        java = Path(os.environ["JAVA_HOME"]) / "bin"
        (app / "InstallCheck.java").write_text('import ai.originchain.*;import java.util.List;public class InstallCheck{public static void main(String[]a){var db=new Client("https://example.invalid","test");try(var batches=db.rows().writeBatches("app.items",List.of(),new BatchOptions())){}try(var pages=db.rows().pages("app.items","id",new PageOptions())){}}}')
        run([java / "javac", "-cp", cp, "InstallCheck.java"], app)
        run([java / "java", "-cp", cp + os.pathsep + str(app), "InstallCheck"], app)
    elif LANGUAGE == "csharp":
        artifact = next(ARTIFACTS.glob("*.nupkg"))
        with zipfile.ZipFile(artifact) as package:
            spec = ET.fromstring(package.read(next(name for name in package.namelist() if name.endswith(".nuspec"))))
        version = next(node.text for node in spec.iter() if node.tag.endswith("}version"))
        (app / "InstallCheck.csproj").write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><PackageReference Include="OriginChainDB" Version="' + version + '" /></ItemGroup></Project>')
        from xml.sax.saxutils import escape
        (app / "NuGet.Config").write_text('<configuration><packageSources><clear/><add key="local" value="' + escape(str(ARTIFACTS), {'"': '&quot;'}) + '"/></packageSources></configuration>')
        (app / "Program.cs").write_text('using OriginChainDB;using var db=new Client("https://example.invalid","test");var pages=db.Rows.PagesAsync("app.items","id",new PageOptions());var batch=new BatchResult(0,1,1);')
        run(["dotnet", "run", "--project", "InstallCheck.csproj", "-c", "Release"], app)
    elif LANGUAGE == "php":
        unpack(next(ARTIFACTS.glob("*.zip")), app / "package")
        module = next((app / "package").rglob("composer.json")).parent
        (app / "composer.json").write_text(json.dumps({"name":"originchain/install-check","require":{"originchain/originchaindb":"1.0.0-rc.1"},"repositories":[{"type":"path","url":module.as_posix(),"options":{"symlink":False,"versions":{"originchain/originchaindb":"1.0.0-rc.1"}}}]}))
        composer = ["php", os.environ["COMPOSER_PHAR"]] if "COMPOSER_PHAR" in os.environ else ["composer"]
        run([*composer, "install", "--no-interaction", "--no-dev"], app)
        (app / "check.php").write_text("<?php require __DIR__.'/vendor/autoload.php'; $db=new OriginChainDB\\Client('https://example.invalid','test'); foreach(['writeBatches','pages','scan'] as $name){if(!method_exists($db->rows,$name)){throw new RuntimeException($name);}} $db->close();")
        run(["php", "check.php"], app)
    elif LANGUAGE == "ruby":
        artifact = next(ARTIFACTS.glob("*.gem"))
        ruby = shutil.which("ruby") or "ruby"
        gem = Path(ruby).parent / "gem"
        run([ruby, gem, "install", artifact, "--install-dir", app / "gems", "--no-document", "--local"], app)
        run([ruby, "-e", "require 'originchaindb'; db=OriginChainDB::Client.new(url:'https://example.invalid',token:'test'); %i[write_batches pages scan].each{|name|raise name.to_s unless db.rows.respond_to?(name)}; db.close"], app, dict(os.environ, GEM_HOME=str(app / "gems"), GEM_PATH=str(app / "gems") + os.pathsep + os.environ.get("GEM_PATH", "")))
    else:
        raise ValueError(LANGUAGE)
print("Fresh package application passed: " + LANGUAGE)
