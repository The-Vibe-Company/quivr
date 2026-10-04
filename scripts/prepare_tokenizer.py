#!/usr/bin/env python3
"""Prepare the pinned offline tokenizer the core.ingest plugin runs; never download model weights or execute model code."""
import hashlib, json, pathlib, platform, subprocess, sys, urllib.request, venv
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

def prepare():
    pinned=requirements()
    work=ROOT/'.scratch/tokenizer';work.mkdir(parents=True,exist_ok=True)
    python=work/'venv/bin/python'
    # Standalone Pythons on Linux and macOS resolve libpython beside the original interpreter; copying it breaks that lookup.
    if not python.exists():venv.EnvBuilder(with_pip=True,symlinks=True).create(work/'venv')
    check=subprocess.run([str(python),'-c','import tokenizers;assert tokenizers.__version__=="0.23.2"'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    if check.returncode:
        subprocess.run([str(python),'-m','pip','install','--only-binary=:all:','--no-deps','--require-hashes','-r',str(pinned)],check=True)
    target=work/'tokenizer.json'
    if not target.exists() or hashlib.sha256(target.read_bytes()).hexdigest()!=PROFILE['tokenizer_sha256']:
        url=f"https://huggingface.co/{PROFILE['model_repository']}/resolve/{PROFILE['model_revision']}/tokenizer.json"
        with urllib.request.urlopen(url,timeout=60) as r: data=r.read(32*1024*1024)
        if hashlib.sha256(data).hexdigest()!=PROFILE['tokenizer_sha256']:raise RuntimeError('tokenizer checksum mismatch')
        staged=work/'tokenizer.download';staged.write_bytes(data);staged.replace(target)
    return dict(python=str(python),model=str(target))
if __name__=='__main__':
    print(json.dumps(prepare()))
