#!/usr/bin/env python3
"""Verify/download the accepted E5 snapshot before starting the offline runtime."""
import hashlib, json, pathlib, platform, shutil, time, urllib.request
ROOT=pathlib.Path(__file__).resolve().parents[1]
MODEL=ROOT/'.scratch/e5-model'
LOCK=json.loads((ROOT/'third_party/e5/model-lock.json').read_text())
def digest(path):
    with path.open('rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()
def prepare():
    if platform.system()!='Linux' or platform.machine()!='x86_64':raise RuntimeError('E5 local reference requires Linux x86_64')
    start=time.monotonic();downloaded=0
    for name,sha in LOCK['files'].items():
        target=MODEL/name
        if target.exists() and digest(target)==sha:continue
        target.parent.mkdir(parents=True,exist_ok=True)
        temporary=target.with_suffix(target.suffix+'.partial')
        url=f"https://huggingface.co/intfloat/multilingual-e5-small/resolve/{LOCK['model_revision']}/{name}"
        with urllib.request.urlopen(url,timeout=120) as response, temporary.open('wb') as out:shutil.copyfileobj(response,out)
        if digest(temporary)!=sha:raise RuntimeError('Pinned model checksum mismatch: '+name)
        downloaded+=temporary.stat().st_size;temporary.replace(target)
    return {'model_revision':LOCK['model_revision'],'tei_image':LOCK['tei_image'],'files':LOCK['files'],'downloaded_bytes':downloaded,'prepare_seconds':round(time.monotonic()-start,3),'architecture':'linux/amd64','device':'CPU','dtype':'float32','backend':'ONNX'}
if __name__=='__main__':print(json.dumps(prepare(),indent=2))
