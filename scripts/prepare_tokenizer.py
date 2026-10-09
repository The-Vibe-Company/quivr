#!/usr/bin/env python3
"""Prepare pinned offline tokenizers for core.ingest or hosted models; never download weights or execute model code."""
import argparse, hashlib, json, os, pathlib, platform, re, subprocess, sys, tempfile, urllib.request, venv
ROOT=pathlib.Path(__file__).resolve().parents[1]
PROFILE=json.loads((ROOT/'plugins/core-ingest/profile.json').read_text())
# The hosts the local stack (make dev) runs on, each with the hash-pinned tokenizers wheel it installs.
SUPPORTED={('Linux','x86_64'):'requirements-linux-x86_64.txt',('Darwin','arm64'):'requirements-macos-arm64.txt'}

def requirements(host=None):
    """The pinned wheel requirements of this host; any other host fails fast, naming the supported ones."""
    system,machine=host or (platform.system(),platform.machine())
    if (system,machine) not in SUPPORTED:
        raise RuntimeError(f'The local stack runs on Linux x86_64 and on macOS with Apple Silicon (arm64); this machine is {system} {machine}.')
    return ROOT/'third_party/tokenizer'/SUPPORTED[system,machine]

def prepare_runtime(hosted=False):
    pinned=requirements()
    work=ROOT/'.scratch/tokenizer';work.mkdir(parents=True,exist_ok=True)
    python=work/'venv/bin/python'
    # Standalone Pythons on Linux and macOS resolve libpython beside the original interpreter; copying it breaks that lookup.
    if not python.exists():venv.EnvBuilder(with_pip=True,symlinks=True).create(work/'venv')
    check=subprocess.run([str(python),'-c','import tokenizers;assert tokenizers.__version__=="0.23.2"'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    if check.returncode:
        options = {'stdout': sys.stderr} if hosted else {}
        command = [str(python), '-m', 'pip', 'install', '--only-binary=:all:',
                   '--no-deps', '--require-hashes', '-r', str(pinned)]
        # Docker copies this script alone; use the repository fetch boundary only in CI checkouts.
        if os.environ.get('GITHUB_ACTIONS') == 'true':
            command = [sys.executable, str(ROOT/'scripts/ci_fetch.py'), '--', *command]
        subprocess.run(command, check=True, **options)
    return python

def prepare():
    python=prepare_runtime()
    work=ROOT/'.scratch/tokenizer'
    target=work/'tokenizer.json'
    if not target.exists() or hashlib.sha256(target.read_bytes()).hexdigest()!=PROFILE['tokenizer_sha256']:
        url=f"https://huggingface.co/{PROFILE['model_repository']}/resolve/{PROFILE['model_revision']}/tokenizer.json"
        with urllib.request.urlopen(url,timeout=60) as r: data=r.read(32*1024*1024)
        if hashlib.sha256(data).hexdigest()!=PROFILE['tokenizer_sha256']:raise RuntimeError('tokenizer checksum mismatch')
        staged=work/'tokenizer.download';staged.write_bytes(data);staged.replace(target)
    return dict(python=str(python),model=str(target))
def prepare_hosted(repository, revision, sha256, output=None):
    # Only immutable repository snapshots, never a URL or moving branch.
    parts = repository.split('/')
    if (len(parts) not in (1, 2) or len(repository) > 96 or '--' in repository or '..' in repository
            or repository.endswith('.git')
            or any(not re.fullmatch(r'[A-Za-z0-9_](?:[A-Za-z0-9_.-]*[A-Za-z0-9_])?', part) for part in parts)):
        raise ValueError('repository must be a Hugging Face model or owner/model name')
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('revision must be a full lowercase commit SHA')
    if not re.fullmatch(r'[0-9a-f]{64}', sha256):
        raise ValueError('sha256 must be a lowercase SHA-256 checksum')
    python = prepare_runtime(hosted=True)
    target = pathlib.Path(output).resolve() if output else ROOT / '.scratch/tokenizer' / (sha256 + '.json')
    if not target.exists() or hashlib.sha256(target.read_bytes()).hexdigest() != sha256:
        url = f'https://huggingface.co/{repository}/resolve/{revision}/tokenizer.json'
        limit = 64 * 1024 * 1024
        with urllib.request.urlopen(url, timeout=60) as response:
            data = response.read(limit + 1)
        if len(data) > limit:
            raise RuntimeError('hosted tokenizer exceeds 64 MiB')
        if hashlib.sha256(data).hexdigest() != sha256:
            raise RuntimeError('hosted tokenizer checksum mismatch')
        target.parent.mkdir(parents=True, exist_ok=True)
        # Unique staging keeps simultaneous preparations from sharing a partial file.
        with tempfile.NamedTemporaryFile(dir=target.parent, delete=False) as staged:
            staged_path = pathlib.Path(staged.name)
            try:
                staged.write(data)
                staged.flush()
                staged_path.chmod(0o644)
                staged_path.replace(target)
            finally:
                staged_path.unlink(missing_ok=True)
    return dict(python=str(python), model=str(target), sha256=sha256)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--hosted', action='store_true', help='Prepare a configured hosted model tokenizer')
    parser.add_argument('--repository', help='Hugging Face model or owner/model containing tokenizer.json')
    parser.add_argument('--revision', help='Full immutable repository commit SHA')
    parser.add_argument('--sha256', help='Expected tokenizer.json SHA-256')
    parser.add_argument('--output', type=pathlib.Path, help='Optional tokenizer.json destination')
    args = parser.parse_args(argv)
    if args.hosted:
        if not all([args.repository, args.revision, args.sha256]):
            parser.error('--hosted requires --repository, --revision and --sha256')
        try:
            config = prepare_hosted(args.repository, args.revision, args.sha256, args.output)
        except ValueError as error:
            parser.error(str(error))
    else:
        if any([args.repository, args.revision, args.sha256, args.output]):
            parser.error('model pins and --output require --hosted')
        config = prepare()
    print(json.dumps(config))


if __name__=='__main__':
    main()
