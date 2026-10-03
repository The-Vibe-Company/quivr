"""Certify missing SDK Contributions against normative fixtures, offline.

Called by the SDK check scripts with their prepared interpreter and CLI.
Artifacts stay under .scratch/plugin-sdk. The Contract Runner remains the
independent oracle for wire output, retries, discovery and invalid requests.
"""
from __future__ import annotations

import json
from pathlib import Path
import subprocess
import sys

import yaml

ROOT = Path(__file__).resolve().parents[1]
FIXTURES = ROOT / "contracts/plugins/v0/fixtures"


def certify(language: str, quivr: str, executable: str) -> None:
    work = ROOT / ".scratch/plugin-sdk" / f"{language}-conformance"
    work.mkdir(parents=True, exist_ok=True)
    if language == "go":
        manifest = yaml.safe_load((FIXTURES / "manifests/valid/subscription.yaml").read_text())
        manifest["contributions"]["normalizer"] = {"media_types": ["text/markdown"]}
        manifest["run"] = {"command": [str(Path(executable).absolute())]}
        fixtures = [FIXTURES / "subscriptions/strike.json"]
    else:
        manifest = yaml.safe_load((FIXTURES / "manifests/valid/connector-push.yaml").read_text())
        ingestion = yaml.safe_load((FIXTURES / "manifests/valid/ingestion.yaml").read_text())
        manifest["contributions"]["ingestion"] = ingestion["contributions"]["ingestion"]
        spaces = manifest["contributions"]["ingestion"]["spaces"]
        manifest["contributions"]["ingestion"]["spaces"] = {name.replace("example.words", manifest["id"]): value for name, value in spaces.items()}
        manifest["run"] = {"command": [str(Path(executable).absolute()), str(ROOT / "scripts/plugin_sdk_conformance_python.py")]}

        connector = manifest["contributions"]["connector"]
        connector["attachments"] = {"max_bytes": 1024}
        connector["kinds"]["files"] = {"config_schema": {"type": "object"}, "default_interval_seconds": 300}
        attachment_fixture = work / "attachment.json"
        attachment_fixture.write_text(json.dumps({"connector": {"kind": "files", "config": {}}, "expect": {"pages": [{"record_keys": ["file-1"], "more": False}]}}))
        fixtures = [FIXTURES / "connectors/push.json", attachment_fixture]
    manifest["compatibility"]["plugin_api"] = ">=0.13.0 <0.14.0"
    path = work / "quivr-plugin.yaml"
    path.write_text(yaml.safe_dump(manifest, sort_keys=False))
    report = work / "contract-report.json"
    command = [quivr, "plugin", "test", "--report", str(report)]
    for fixture in fixtures:
        command += ["--fixture", str(fixture)]
    result = subprocess.run([*command, str(work)], check=False, capture_output=True, text=True)
    (work / "contract.log").write_text(result.stdout + result.stderr)
    if result.returncode:
        raise RuntimeError(result.stdout + result.stderr)
    document = json.loads(report.read_text())
    checks = document["checks"]
    expected = {"normalizer", "subscription"} if language == "go" else {"connector", "ingestion"}
    exercised = {c.get("contribution") for c in checks if c["id"] == "invoke" and c["status"] == "pass"}
    if not expected <= exercised:
        raise RuntimeError(f"missing certification: {expected - exercised}")
    if language == "python" and not any(c["id"] == "attachments" and c["status"] == "pass" and "1 uploaded" in c.get("note", "") for c in checks):
        raise RuntimeError("attachment bytes were not certified")
    print(f"Certified {language} SDK normative Contributions: {report}")


if __name__ == "__main__":
    certify(*sys.argv[1:])
