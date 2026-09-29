"""Check that the Railway images copy every file their programs need.

Core: the build stage must copy every Go package ./cmd/quivr imports.
Web: the runtime stage must copy every module quivr-search/server.mjs imports.

The local stack builds the quivr binary natively, so nothing else notices when
a new top-level Go package is imported but not copied by
deploy/railway/core.Dockerfile, and the hosted image then fails to build.
This copies exactly the build stage's COPY sources into a temporary directory
and builds ./cmd/quivr there. It also checks that every other stage's COPY
source from the build context exists, such as the plugins the image bakes in.
Every first-party Go connector plugin (plugins/<id> with a go.mod) must build
the same way from exactly the COPY sources of the ``connectors`` stage.

    python3 scripts/image_context.py [Dockerfile]   # guard used by make verify
"""
import fnmatch
import os
import re
import pathlib
import shlex
import shutil
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent
DOCKERFILE = ROOT / 'deploy' / 'railway' / 'core.Dockerfile'
WEB_DOCKERFILE = ROOT / 'deploy' / 'railway' / 'web.Dockerfile'
WEB_ENTRY = 'quivr-search/server.mjs'
IMPORT = re.compile(r"""(?:from\s+|import\s*\(\s*|import\s+)['"](\.{1,2}/[^'"]+)['"]""")


def instructions(text):
    """Yield each instruction's words, joining backslash-continued lines."""
    pending = ''
    for line in text.splitlines():
        if line.rstrip().endswith('\\'):
            pending += line.rstrip()[:-1] + ' '
            continue
        words = shlex.split(pending + line, comments=True)
        pending = ''
        if words:
            yield words


CONNECTOR_STAGE = 'connectors'


def build_stage_copies(text):
    """Return (source, destination) pairs copied by the first stage from the build context."""
    return stage_copies(text)


def stage_copies(text, name=None):
    """Return (source, destination) pairs copied from the build context by the stage
    named ``name`` (``FROM image AS name``), or by the first stage; None when no stage has that name."""
    copies, stage, selected = [], 0, False
    for words in instructions(text):
        instruction = words[0].upper()
        if instruction == 'FROM':
            stage += 1
            if selected:
                break
            label = words[-1] if len(words) >= 4 and words[-2].upper() == 'AS' else None
            selected = (label == name) if name else stage == 1
        elif selected and instruction == 'COPY' and not any(w.startswith('--from') for w in words[1:]):
            args = [w for w in words[1:] if not w.startswith('--')]
            *sources, destination = args
            for source in sources:
                if len(sources) > 1 or destination.endswith('/'):
                    copies.append((source, str(pathlib.PurePosixPath(destination) / pathlib.PurePosixPath(source).name)))
                else:
                    copies.append((source, destination))
    return copies if selected or (name is None and stage) else None


def context_sources(text):
    """Return every COPY source taken from the build context, in all stages."""
    sources = []
    for words in instructions(text):
        if words[0].upper() == 'COPY' and not any(w.startswith('--from') for w in words[1:]):
            sources += [w for w in words[1:] if not w.startswith('--')][:-1]
    return sources


def check(root, dockerfile, go):
    """Build ./cmd/quivr from only the build stage's copies; return the failure text or None."""
    for source in context_sources(dockerfile.read_text()):
        if not any(root.glob(source)):
            return f'{dockerfile.name}: COPY source {source} does not exist'
    with tempfile.TemporaryDirectory() as tmp:
        context = pathlib.Path(tmp)
        failure = materialize(root, build_stage_copies(dockerfile.read_text()), context, dockerfile)
        if failure:
            return failure
        result = subprocess.run([go, 'build', '-o', os.devnull, './cmd/quivr'], cwd=context,
                                capture_output=True, text=True, env={**os.environ, 'CGO_ENABLED': '0'})
        if result.returncode != 0:
            return (f'{dockerfile.name}: the image build stage cannot build ./cmd/quivr.\n'
                    f'{result.stderr.strip()}\n'
                    'Fix: COPY every top-level Go package the binary imports into the build stage.')
    return check_connectors(root, dockerfile, go)


def materialize(root, copies, context, dockerfile):
    """Copy (source, destination) pairs into context; return the failure text or None."""
    for source, destination in copies:
        origin, target = root / source, context / destination
        target.parent.mkdir(parents=True, exist_ok=True)
        if origin.is_dir():
            shutil.copytree(origin, target, dirs_exist_ok=True)
        elif origin.exists():
            shutil.copy2(origin, target)
        else:
            return f'{dockerfile.name}: COPY source {source} does not exist'
    return None


def go_plugins(root):
    """The first-party Go connector plugins: plugins/<id> directories with a go.mod."""
    return sorted(p.parent.relative_to(root).as_posix() for p in root.glob('plugins/*/go.mod'))


def check_connectors(root, dockerfile, go):
    """Build every Go connector plugin from only the connectors stage's copies; return the failure text or None."""
    plugins = go_plugins(root)
    if not plugins:
        return None
    copies = stage_copies(dockerfile.read_text(), CONNECTOR_STAGE)
    if copies is None:
        return (f'{dockerfile.name}: no "{CONNECTOR_STAGE}" stage builds the Go connector plugins {", ".join(plugins)}.\n'
                f'Fix: add a stage "FROM golang AS {CONNECTOR_STAGE}" that copies them and builds each into /out/bin/quivr-<id>.')
    with tempfile.TemporaryDirectory() as tmp:
        context = pathlib.Path(tmp)
        failure = materialize(root, copies, context, dockerfile)
        if failure:
            return failure
        for plugin in plugins:
            if not (context / plugin / 'go.mod').exists():
                return (f'{dockerfile.name}: the {CONNECTOR_STAGE} stage does not copy {plugin}.\n'
                        f'Fix: COPY {plugin} into the {CONNECTOR_STAGE} stage.')
            result = subprocess.run([go, 'build', '-o', os.devnull, '.'], cwd=context / plugin,
                                    capture_output=True, text=True, env={**os.environ, 'CGO_ENABLED': '0'})
            if result.returncode != 0:
                return (f'{dockerfile.name}: the {CONNECTOR_STAGE} stage cannot build {plugin}.\n'
                        f'{result.stderr.strip()}\n'
                        f'Fix: COPY every directory the plugin module needs (its own tree and sdks/go) into the {CONNECTOR_STAGE} stage.')
    return None


def runtime_stage_sources(text):
    """Return the build-context sources copied by the last stage (not --from)."""
    stages, current = [], []
    for words in instructions(text):
        if words[0].upper() == 'FROM':
            current = []
            stages.append(current)
        elif words[0].upper() == 'COPY' and current is not None and not any(w.startswith('--from') for w in words[1:]):
            current.extend([w for w in words[1:] if not w.startswith('--')][:-1])
    return stages[-1] if stages else []


def check_web(root, dockerfile, entry=WEB_ENTRY):
    """Return the failure text when a module the web server imports is not copied, or None."""
    sources = runtime_stage_sources(dockerfile.read_text())
    def copied(path):
        return any(fnmatch.fnmatch(path, s) or path.startswith(s.rstrip('/') + '/') for s in sources)
    seen, todo, missing = set(), [entry], []
    while todo:
        path = todo.pop()
        if path in seen:
            continue
        seen.add(path)
        if not copied(path):
            missing.append(path)
        for spec in IMPORT.findall((root / path).read_text()):
            target = (pathlib.PurePosixPath(path).parent / spec).as_posix()
            target = os.path.normpath(target)
            if (root / target).is_file():
                todo.append(target)
    if missing:
        return (f'{dockerfile.name}: the runtime stage does not copy {", ".join(sorted(missing))}, '
                f'which {entry} imports.\nFix: COPY every server module into the runtime stage.')
    return None


def main(argv):
    dockerfile = pathlib.Path(argv[1]).resolve() if len(argv) > 1 else DOCKERFILE
    failure = check(ROOT, dockerfile, os.environ.get('GO', 'go'))
    if not failure and len(argv) <= 1:
        failure = check_web(ROOT, WEB_DOCKERFILE)
    if failure:
        print(failure, file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
