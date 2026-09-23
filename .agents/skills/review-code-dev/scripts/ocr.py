#!/usr/bin/env python3
"""Run pinned OCR delegation on macOS/Linux; bootstrap without Node or LLM config."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile
import urllib.request

VERSION = "1.12.1"
CHECKSUMS = {
    "darwin-arm64": "55b305965d946e3b53a701809a118d2013cd03082bd0941ffe9c974832819c1c",
    "darwin-amd64": "a0c02c70db535d5995e991af2a8141d7af787242a557be236e233bea3333d654",
    "linux-arm64": "b7b22f879048dc642f334af180d84040a1615ba912172ae242ba514851ee9cb8",
    "linux-amd64": "5e17687c60646846cc942f7422d2ae9e7104f7315ad973505cc73bac56bd62f1",
}


def target(system: str, machine: str) -> str:
    arch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(machine.lower())
    key = f"{system.lower()}-{arch}"
    if key not in CHECKSUMS:
        raise RuntimeError(f"Unsupported OCR platform: {system}/{machine}; an upstream-supported CLI build is required.")
    return key


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def ensure_binary() -> Path:
    key = target(platform.system(), platform.machine())
    root = Path(os.environ.get("REVIEW_CODE_OCR_HOME", str(Path.home() / ".local/share/review-code-dev/ocr"))).expanduser().resolve()
    binary = root / VERSION / key / "ocr"
    if binary.exists():
        if digest(binary) != CHECKSUMS[key]:
            raise RuntimeError(f"OCR checksum mismatch: {binary}. Refusing to execute; inspect the cached file.")
        return binary
    binary.parent.mkdir(parents=True, exist_ok=True)
    url = f"https://github.com/alibaba/open-code-review/releases/download/v{VERSION}/opencodereview-{key}"
    print(f"Installing OCR {VERSION} ({key}) into {binary.parent}", file=sys.stderr)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=binary.parent, prefix=".download-", delete=False) as out:
            temporary = Path(out.name)
            with urllib.request.urlopen(url, timeout=60) as response:
                while chunk := response.read(1024 * 1024):
                    out.write(chunk)
        if digest(temporary) != CHECKSUMS[key]:
            raise RuntimeError("Downloaded OCR checksum mismatch; refusing to install.")
        temporary.chmod(0o755)
        temporary.replace(binary)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
    return binary


def main(args: list[str]) -> int:
    if args == ["--help"] or not args:
        print("Usage: python3 ocr.py version | delegate preview [flags] | delegate rule [flags] -- <paths>\n"
              "First call downloads checksum-pinned OCR. No API key, Node, sudo, or PATH change.\n"
              "Optional REVIEW_CODE_OCR_HOME overrides the user cache directory.")
        return 0
    if args != ["version"] and args[:2] not in (["delegate", "preview"], ["delegate", "rule"]):
        raise RuntimeError("Only version and delegate preview/rule are supported; this wrapper never configures or calls an LLM.")
    binary = ensure_binary()
    env = dict(os.environ, OCR_NO_UPDATE="1")
    return subprocess.run([str(binary), *args], env=env, check=False).returncode


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except (OSError, RuntimeError) as exc:
        print(f"OCR unavailable: {exc}\nReport the setup error; delegation does not require LLM credentials.", file=sys.stderr)
        sys.exit(1)
