"""Check that the Railway images copy every file their programs need.

Core: the build stage must copy every Go package ./cmd/quivr imports.
Web: the runtime stage must copy every module quivr-search/server.mjs imports.
TEI: the stage that copies scripts/prepare_embeddings.py must copy every
repository module the script imports.

The local stack builds the quivr binary natively, so nothing else notices when
a new top-level Go package is imported but not copied by
deploy/railway/core.Dockerfile, and the hosted image then fails to build.
This copies exactly the build stage's COPY sources into a temporary directory
and builds ./cmd/quivr there. It also checks that every other stage's COPY
source from the build context exists, such as the plugins the image bakes in.
The Python plugins stage must install from its own COPY sources with its pip
constraints, including optional retrieval sidecars.
Every first-party Go connector plugin (plugins/<id> with a go.mod) must build
the same way from exactly the COPY sources of the ``connectors`` stage.

    python3 scripts/image_context.py [Dockerfile]   # guard used by make verify
"""
import ast
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
ENGINE_DOCKERFILE = ROOT / 'deploy' / 'images' / 'quivr.Dockerfile'
WEB_DOCKERFILE = ROOT / 'deploy' / 'railway' / 'web.Dockerfile'
WEB_ENTRY = 'quivr-search/server.mjs'
TEI_DOCKERFILE = ROOT / 'deploy' / 'railway' / 'tei.Dockerfile'
TEI_ENTRY = 'scripts/prepare_embeddings.py'
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


def check_engine(root, dockerfile, go):
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
    return None


def check(root, dockerfile, go):
    return (check_engine(root, dockerfile, go) or check_connectors(root, dockerfile, go)
            or check_python_plugins(root, dockerfile))


def materialize(root, copies, context, dockerfile):
    """Copy (source, destination) pairs into context; return the failure text or None."""
    for source, destination in copies:
        origin, target = root / source, context / destination.lstrip('/')
        target.parent.mkdir(parents=True, exist_ok=True)
        if origin.is_dir():
            shutil.copytree(origin, target, dirs_exist_ok=True)
        elif origin.exists():
            shutil.copy2(origin, target)
        else:
            return f'{dockerfile.name}: COPY source {source} does not exist'
    return None


def check_python_plugins(root, dockerfile):
    """Install the Python sidecars from exactly the plugins stage's context and pip inputs."""
    text = dockerfile.read_text()
    copies = stage_copies(text, 'plugins')
    if copies is None:
        return None
    selected, install = False, None
    for words in instructions(text):
        if words[0].upper() == 'FROM':
            selected = len(words) >= 4 and words[-2].upper() == 'AS' and words[-1] == 'plugins'
        elif selected and words[0].upper() == 'RUN':
            for i, word in enumerate(words[:-1]):
                if pathlib.PurePosixPath(word).name == 'pip' and words[i + 1] == 'install':
                    install = words[i + 2:]
                    if '&&' in install:
                        install = install[:install.index('&&')]
    if install is None:
        return f'{dockerfile.name}: the plugins stage has no pip install command'
    with tempfile.TemporaryDirectory() as tmp:
        context = pathlib.Path(tmp)
        failure = materialize(root, copies, context, dockerfile)
        if failure:
            return failure
        # Absolute image paths resolve inside the copied context, never against the host.
        args = [str(context / word.lstrip('/')) if word.startswith('/') else word for word in install]
        environment = context / '.image-context-venv'
        result = subprocess.run([sys.executable, '-m', 'venv', str(environment)], capture_output=True, text=True)
        if result.returncode:
            return f'{dockerfile.name}: cannot prepare the Python plugins environment:\n{result.stderr.strip()}'
        python = str(environment / 'bin' / 'python')
        if os.environ.get('GITHUB_ACTIONS') == 'true' and '--no-index' not in args:
            # Validate image package metadata offline against the locked CI inputs.
            # Keep resolution enabled: a new/missing dependency must fail this guard.
            locked = subprocess.run([sys.executable, str(ROOT / 'scripts/ci_fetch.py'), '--', python,
                                     '-m', 'pip', 'install', '--require-hashes', '-r',
                                     str(ROOT / 'scripts/ci-requirements-sdk.txt')],
                                    cwd=ROOT, capture_output=True, text=True)
            if locked.returncode:
                return f'{dockerfile.name}: cannot prepare locked image dependencies:\n{locked.stdout}{locked.stderr}'
            args += ['--no-index', '--no-build-isolation']
        command = [python, '-m', 'pip', 'install', *args]
        if os.environ.get('GITHUB_ACTIONS') == 'true':
            command = [sys.executable, str(ROOT / 'scripts/ci_fetch.py'), '--', *command]
        result = subprocess.run(command, cwd=context, capture_output=True, text=True)
        if result.returncode:
            return (f'{dockerfile.name}: the plugins stage cannot install its Python packages.\n'
                    f'{(result.stderr or result.stdout).strip()}\nFix: COPY each package and constraint file used by pip into the plugins stage.')
        result = subprocess.run([python, '-m', 'pip', 'check'], cwd=context, capture_output=True, text=True)
        if result.returncode:
            return f'{dockerfile.name}: incompatible Python plugin dependencies:\n{result.stdout.strip()}'
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
            environment = {**os.environ, 'CGO_ENABLED': '0'}
            if os.environ.get('GITHUB_ACTIONS') == 'true':
                fetched = subprocess.run([sys.executable, str(ROOT/'scripts/ci_fetch.py'), '--', go, 'mod', 'download'],
                                         cwd=root / plugin, capture_output=True, text=True)
                if fetched.returncode:
                    return f'{dockerfile.name}: cannot fetch pinned {plugin} dependencies:\n{fetched.stdout}{fetched.stderr}'
                environment['GOPROXY'] = 'off'
            result = subprocess.run([go, 'build', '-o', os.devnull, '.'], cwd=context / plugin,
                                    capture_output=True, text=True, env=environment)
            if result.returncode != 0:
                return (f'{dockerfile.name}: the {CONNECTOR_STAGE} stage cannot build {plugin}.\n'
                        f'{result.stderr.strip()}\n'
                        f'Fix: COPY every directory the plugin module needs (its own tree and sdks/go) into the {CONNECTOR_STAGE} stage.')
    return None


def stages_sources(text):
    """Return, for each stage, the build-context sources it copies (not --from)."""
    stages, current = [], None
    for words in instructions(text):
        if words[0].upper() == 'FROM':
            current = []
            stages.append(current)
        elif words[0].upper() == 'COPY' and current is not None and not any(w.startswith('--from') for w in words[1:]):
            current.extend([w for w in words[1:] if not w.startswith('--')][:-1])
    return stages


def runtime_stage_sources(text):
    """Return the build-context sources copied by the last stage (not --from)."""
    stages = stages_sources(text)
    return stages[-1] if stages else []


def copied(path, sources):
    """Whether a COPY source (a file, a directory or a glob) brings path into the image."""
    return any(fnmatch.fnmatch(path, s) or path.startswith(s.rstrip('/') + '/') for s in sources)


def check_web(root, dockerfile, entry=WEB_ENTRY):
    """Return the failure text when a module the web server imports is not copied, or None."""
    sources = runtime_stage_sources(dockerfile.read_text())
    seen, todo, missing = set(), [entry], []
    while todo:
        path = todo.pop()
        if path in seen:
            continue
        seen.add(path)
        if not copied(path, sources):
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


def python_imports(root, path):
    """Yield the repository modules a script imports; Python resolves them next to the script."""
    folder = pathlib.PurePosixPath(path).parent
    for node in ast.walk(ast.parse((root / path).read_text(), path)):
        if isinstance(node, ast.Import):
            names = [alias.name for alias in node.names]
        elif isinstance(node, ast.ImportFrom) and not node.level and node.module:
            names = [node.module]
        else:
            continue
        for name in names:
            top = name.split('.')[0]
            for candidate in (folder / f'{top}.py', folder / top / '__init__.py'):
                if (root / candidate).is_file():
                    yield candidate.as_posix()


def check_python(root, dockerfile, entry):
    """Return the failure text when the stage that copies a script misses a module it imports, or None."""
    sources = next((s for s in stages_sources(dockerfile.read_text()) if copied(entry, s)), None)
    if sources is None:
        return f'{dockerfile.name}: no stage copies {entry}.'
    seen, todo, missing = set(), [entry], []
    while todo:
        path = todo.pop()
        if path in seen:
            continue
        seen.add(path)
        if not copied(path, sources):
            missing.append(path)
        todo.extend(python_imports(root, path))
    if missing:
        return (f'{dockerfile.name}: the stage that copies {entry} does not copy {", ".join(sorted(missing))}, '
                f'which the script imports.\nFix: COPY every module the script imports into that stage, '
                f'or keep {entry} standalone.')
    return None


def main(argv):
    dockerfile = pathlib.Path(argv[1]).resolve() if len(argv) > 1 else DOCKERFILE
    failure = check(ROOT, dockerfile, os.environ.get('GO', 'go'))
    if not failure and len(argv) <= 1:
        failure = (check_engine(ROOT, ENGINE_DOCKERFILE, os.environ.get('GO', 'go'))
                   or check_web(ROOT, WEB_DOCKERFILE) or check_python(ROOT, TEI_DOCKERFILE, TEI_ENTRY)
                   or check_python(ROOT, DOCKERFILE, 'scripts/prepare_tokenizer.py')
                   or check_python(ROOT, ROOT/'deploy/images/plugin.Dockerfile', 'scripts/prepare_tokenizer.py'))
    if failure:
        print(failure, file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
