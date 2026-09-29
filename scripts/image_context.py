"""Check that the core image's build stage copies every Go package it needs.

The local stack builds the quivr binary natively, so nothing else notices when
a new top-level Go package is imported but not copied by
deploy/railway/core.Dockerfile, and the hosted image then fails to build.
This copies exactly the build stage's COPY sources into a temporary directory
and builds ./cmd/quivr there.

    python3 scripts/image_context.py [Dockerfile]   # guard used by make verify
"""
import os
import pathlib
import shlex
import shutil
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent
DOCKERFILE = ROOT / 'deploy' / 'railway' / 'core.Dockerfile'


def build_stage_copies(text):
    """Return (source, destination) pairs copied by the first stage from the build context."""
    copies, stage = [], 0
    for line in text.splitlines():
        words = shlex.split(line, comments=True)
        if not words:
            continue
        instruction = words[0].upper()
        if instruction == 'FROM':
            stage += 1
            if stage > 1:
                break
        elif instruction == 'COPY' and not any(w.startswith('--from') for w in words[1:]):
            args = [w for w in words[1:] if not w.startswith('--')]
            *sources, destination = args
            for source in sources:
                if len(sources) > 1 or destination.endswith('/'):
                    copies.append((source, str(pathlib.PurePosixPath(destination) / pathlib.PurePosixPath(source).name)))
                else:
                    copies.append((source, destination))
    return copies


def check(root, dockerfile, go):
    """Build ./cmd/quivr from only the build stage's copies; return the failure text or None."""
    with tempfile.TemporaryDirectory() as tmp:
        context = pathlib.Path(tmp)
        for source, destination in build_stage_copies(dockerfile.read_text()):
            origin, target = root / source, context / destination
            target.parent.mkdir(parents=True, exist_ok=True)
            if origin.is_dir():
                shutil.copytree(origin, target, dirs_exist_ok=True)
            elif origin.exists():
                shutil.copy2(origin, target)
            else:
                return f'{dockerfile.name}: COPY source {source} does not exist'
        result = subprocess.run([go, 'build', '-o', os.devnull, './cmd/quivr'], cwd=context,
                                capture_output=True, text=True, env={**os.environ, 'CGO_ENABLED': '0'})
        if result.returncode != 0:
            return (f'{dockerfile.name}: the image build stage cannot build ./cmd/quivr.\n'
                    f'{result.stderr.strip()}\n'
                    'Fix: COPY every top-level Go package the binary imports into the build stage.')
    return None


def main(argv):
    dockerfile = pathlib.Path(argv[1]).resolve() if len(argv) > 1 else DOCKERFILE
    failure = check(ROOT, dockerfile, os.environ.get('GO', 'go'))
    if failure:
        print(failure, file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
