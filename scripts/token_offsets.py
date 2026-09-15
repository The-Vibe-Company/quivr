#!/usr/bin/env python3
"""Bounded offline tokenizer subprocess. stdout is protocol JSON, never diagnostics or source logs."""
import hashlib, json, pathlib, sys
import tokenizers
from tokenizers import Tokenizer

def main():
    if tokenizers.__version__!='0.23.2':raise ValueError('tokenizer version mismatch')
    data=pathlib.Path(sys.argv[1]).read_bytes()
    if hashlib.sha256(data).hexdigest()!='0b44a9d7b51c3c62626640cda0e2c2f70fdacdc25bbbd68038369d14ebdf4c39':raise ValueError('tokenizer checksum mismatch')
    tokenizer=Tokenizer.from_str(data.decode('utf-8'));tokenizer.no_truncation();tokenizer.no_padding()
    request=json.loads(sys.stdin.buffer.read(4*1024*1024+1))
    if len(request)>512 or sum(len(x['text'].encode('utf-8')) for x in request)>2*1024*1024:raise ValueError('tokenizer batch limit')
    result=[]
    for item in request:
        encoded=tokenizer.encode(item['text'],add_special_tokens=item['special'])
        result.append(dict(tokens=len(encoded.ids),offsets=encoded.offsets))
    json.dump(result,sys.stdout,ensure_ascii=True)
try:main()
except Exception:
    sys.stderr.write('tokenizer request failed\n');sys.exit(1)
