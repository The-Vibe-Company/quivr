#!/usr/bin/env python3
"""Prepare the pinned offline tokenizer; never download model weights or execute model code."""
import hashlib, json, pathlib, platform, subprocess, sys, urllib.request, venv
ROOT=pathlib.Path(__file__).resolve().parents[1]
PROFILE=json.loads((ROOT/'internal/processing/profile.json').read_text())

def prepare():
    if platform.system()!='Linux' or platform.machine()!='x86_64':
        raise RuntimeError('tokenizer harness currently supports Linux x86_64 only')
    work=ROOT/'.scratch/tokenizer';work.mkdir(parents=True,exist_ok=True)
    python=work/'venv/bin/python'
    if not python.exists():venv.EnvBuilder(with_pip=True).create(work/'venv')
    check=subprocess.run([str(python),'-c','import tokenizers;assert tokenizers.__version__=="0.23.2"'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    if check.returncode:
        subprocess.run([str(python),'-m','pip','install','--only-binary=:all:','--no-deps','--require-hashes','-r',str(ROOT/'third_party/tokenizer/requirements-linux-x86_64.txt')],check=True)
    target=work/'tokenizer.json'
    if not target.exists() or hashlib.sha256(target.read_bytes()).hexdigest()!=PROFILE['tokenizer_sha256']:
        url=f"https://huggingface.co/{PROFILE['model_repository']}/resolve/{PROFILE['model_revision']}/tokenizer.json"
        with urllib.request.urlopen(url,timeout=60) as r: data=r.read(32*1024*1024)
        if hashlib.sha256(data).hexdigest()!=PROFILE['tokenizer_sha256']:raise RuntimeError('tokenizer checksum mismatch')
        staged=work/'tokenizer.download';staged.write_bytes(data);staged.replace(target)
    return dict(python=str(python),script=str(ROOT/'scripts/token_offsets.py'),model=str(target))
if __name__=='__main__':
    print(json.dumps(prepare()))
