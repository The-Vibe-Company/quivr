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

VERSION = "1.13.12"
CHECKSUMS = {
    "darwin-arm64": "d68eadd45e57f05698373cc7115084a204798e7da37a52ce8326444a9e257a65",
    "darwin-amd64": "e067e92e8fa7030f59490f9cec5b86f9c69dd8a13d252b979f4cd87b7f047696",
    "linux-arm64": "178b1a926616f69522a7dc18a69513841813132f98accc35db960276cd53408d",
    "linux-amd64": "673ed6bfd905bf68383269e78945b9d873a5c8c0931fce6e86374ca58df0b8b1",
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
