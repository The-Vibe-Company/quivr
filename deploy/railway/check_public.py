#!/usr/bin/env python3
"""Verify real public ingestion/search and retention before/after service restarts."""
import argparse
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import time
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('phase', choices=['before', 'after'])
    parser.add_argument('--url', required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    args = parser.parse_args()
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    def call(path, data=None):
        request = urllib.request.Request(args.url.rstrip('/') + path,
            data=None if data is None else json.dumps(data).encode(),
            headers={'Content-Type': 'application/json'})
        with client.open(request, timeout=30) as response:
            return json.load(response)
    call('/demo/login', {'password': os.environ['QUIVR_DEMO_PASSWORD']})
    assert any(c.secure and c.has_nonstandard_attr('HttpOnly') for c in jar), 'Expected Secure HttpOnly session'
    corpus = call('/demo/session')['corpus_id']
    if args.phase == 'before':
        identity = uuid.uuid4().hex
        text = f'La bibliothèque de Quivr conserve les notes de voyage.\n\nÉtoile 🌟 : départ pour Marseille, puis la Corse. Repère {identity}.'
        receipt = call('/v0/records', {'idempotency_key': identity,
            'source': {'corpus_id': corpus, 'namespace': 'web-demo', 'record_key': identity},
            'content': {'kind': 'text', 'text': text}})
        deadline = time.monotonic() + 120
        while not (receipt.get('availability', {}).get('searchable') and receipt['processing']['state'] == 'idle'):
            if time.monotonic() > deadline:
                raise RuntimeError('Ingestion/enrichment did not become ready')
            time.sleep(1)
            receipt = call('/v0/ingestion-receipts/' + receipt['receipt_id'])
        evidence = {'url': args.url, 'corpus_id': corpus, 'receipt_id': receipt['receipt_id'],
            'record_id': receipt['record_id'], 'version_id': receipt['version_id'], 'text': text, 'identity': identity}
    else:
        evidence = json.loads(args.evidence.read_text())
        assert corpus == evidence['corpus_id'], 'Corpus changed across restart'
    version = call(f"/v0/records/{evidence['record_id']}/versions/{evidence['version_id']}")
    assert version['manifest']['parts'][0]['content']['text'] == evidence['text']
    for mode in ['lexical', 'semantic', 'hybrid']:
        response = call('/v0/search', {'corpus_ids': [corpus], 'query': evidence['identity'], 'mode': mode, 'profile': 'default', 'limit': 10})
        assert any(item['version_id'] == evidence['version_id'] for item in response['items']), mode + ' lost the version'
    evidence[args.phase] = {'status': 'passed', 'checked_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
        'source_sha256': hashlib.sha256(evidence['text'].encode()).hexdigest(), 'modes': ['lexical', 'semantic', 'hybrid'], 'secure_session': True}
    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    args.evidence.write_text(json.dumps(evidence, ensure_ascii=False, indent=2))
    print(args.phase + ': public ingestion/source/search verification passed')


if __name__ == '__main__':
    main()
