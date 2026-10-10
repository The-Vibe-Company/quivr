"""Run external dependency fetches through the CI retry and integrity boundary.

Local commands keep their normal resolver behavior. GitHub Actions routes only
the dependency command passed here through ``ci_fetch.py``; builds and tests
must continue to call ``subprocess`` or the existing local runner directly.
"""
import argparse
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]


def in_github_actions():
    return os.environ.get("GITHUB_ACTIONS", "").lower() == "true"


def pip_executable(python):
    return str(Path(python).with_name("pip"))


def run_dependency(command, *, cwd=ROOT, **kwargs):
    command = [os.fspath(value) for value in command]
    if in_github_actions():
        command = [sys.executable, str(ROOT / "scripts" / "ci_fetch.py"), "--", *command]
    return subprocess.run(command, cwd=cwd, check=True, **kwargs)


def install_locked(python, lock, *, cwd=ROOT, quiet=True, **kwargs):
    command = [pip_executable(python), "install"]
    if quiet:
        command.append("-q")
    command += ["--disable-pip-version-check", "--require-hashes", "-r", os.fspath(lock)]
    return run_dependency(command, cwd=cwd, **kwargs)


def install_editable(python, package, *, cwd=ROOT, quiet=True, **kwargs):
    command = [pip_executable(python), "install"]
    if quiet:
        command.append("-q")
    command += ["--disable-pip-version-check", "--no-deps", "--no-build-isolation", "-e", os.fspath(package)]
    return run_dependency(command, cwd=cwd, **kwargs)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cwd", type=Path, default=ROOT)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args(argv)
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        parser.error("a dependency command is required")
    run_dependency(command, cwd=args.cwd)


if __name__ == "__main__":
    main()
