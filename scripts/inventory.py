#!/usr/bin/env python3
"""Pins and dependency inventory of the artifacts `make verify` builds and runs (THE-662).

Usage: python3 scripts/inventory.py [quivr-binary] [output.json]

The inventory states what it could identify and says so explicitly when it could
not: a licence it cannot classify is "unclassified", never guessed. It is an
evaluation-stage notice list, not a legal review or a production certification.
"""
import json, os, pathlib, platform, re, subprocess, sys
ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from deploy import infrastructure

# Declared supported platform of the local harness and CI. Nothing else is claimed.
PLATFORM = 'linux/amd64'
UNSUPPORTED = ['macOS: make dev runs on arm64 (TEI under x86_64 emulation); verification is Linux x86_64 only',
               'linux/arm64: no pinned tokenizer wheel or TEI image digest; not tested']

LICENCE_PATTERNS = [
    ('Apache-2.0', r'Apache License,?\s+Version 2\.0'),
    ('MPL-2.0', r'Mozilla Public License,?\s+(v\.|version)\s*2\.0'),
    ('BSD-3-Clause', r'Neither the name of'),
    ('BSD-2-Clause', r'Redistributions in binary form must reproduce'),
    ('MIT', r'Permission is hereby granted, free of charge'),
    ('ISC', r'Permission to use, copy, modify, and(/or)? distribute this software for any purpose'),
]


def classify(text):
    """Best-effort SPDX guess from a licence file; order matters (BSD-3 also matches BSD-2)."""
    for spdx, pattern in LICENCE_PATTERNS:
        if re.search(pattern, text, re.I):
            return spdx
    return 'unclassified'


def images():
    """Container images pinned by digest in the Compose file and the contract generator."""
    found = re.findall(r'image:\s*(\S+)', (ROOT / 'deploy/compose/compose.yaml').read_text())
    found += [spec['image'] for spec in infrastructure.resolve(environ={}).values() if 'image' in spec]
    found += re.findall(r'(openapitools/openapi-generator-cli:\S+@sha256:[0-9a-f]+)', (ROOT / 'scripts/contracts.sh').read_text())
    return [{'image': i, 'pinned_by_digest': '@sha256:' in i, 'licence': 'see upstream image; not inventoried'} for i in dict.fromkeys(found)]


def model():
    lock = json.loads((ROOT / 'third_party/e5/model-lock.json').read_text())
    return {'name': 'intfloat/multilingual-e5-small', 'revision': lock['model_revision'], 'files': lock['files'],
            'notice': 'third_party/e5/NOTICE.md', 'model_card': 'third_party/e5/MODEL_CARD.md',
            'unresolved': 'Training-data provenance is as published by the upstream model card; not independently verified.'}


def tokenizer():
    reqs = (ROOT / 'third_party/tokenizer/requirements-linux-x86_64.txt').read_text()
    pins = [line.split()[0] for line in reqs.splitlines() if line and not line.startswith('#')]
    return {'python_requirements': pins, 'notice': 'third_party/tokenizer/NOTICE.md', 'licence_file': 'third_party/tokenizer/LICENSE.tokenizers'}


def go_modules(binary):
    """Modules actually linked into the shipped quivr binary, with their licence file."""
    if not binary or not pathlib.Path(binary).exists():
        return {'status': 'not inventoried: binary not built', 'modules': []}
    go = os.environ.get('GO', 'go')
    info = subprocess.run([go, 'version', '-m', str(binary)], capture_output=True, text=True, check=True).stdout
    cache = pathlib.Path(subprocess.run([go, 'env', 'GOMODCACHE'], capture_output=True, text=True, check=True).stdout.strip())
    modules = []
    for line in info.splitlines():
        parts = line.split()
        if len(parts) >= 3 and parts[0] in ('dep', '=>'):
            path, version = parts[1], parts[2]
            escaped = re.sub(r'[A-Z]', lambda m: '!' + m.group(0).lower(), path)
            directory = cache / f'{escaped}@{version}'
            files = sorted(p for p in directory.glob('*') if p.is_file() and re.match(r'(?i)(licen[cs]e|copying|notice)', p.name)) if directory.exists() else []
            licence = classify(files[0].read_text(errors='replace')) if files else 'unclassified (no licence file in module cache)'
            modules.append({'module': path, 'version': version, 'licence': licence, 'files': [f.name for f in files]})
    return {'status': 'linked modules of the quivr binary (go version -m)', 'modules': modules}


def npm_packages():
    lock = json.loads((ROOT / 'quivr-search/package-lock.json').read_text())
    out = []
    for key, value in lock.get('packages', {}).items():
        if key:
            out.append({'package': key.removeprefix('node_modules/'), 'version': value.get('version'), 'dev': bool(value.get('dev')),
                        'licence': value.get('license') or 'unclassified (not recorded in package-lock.json)'})
    return {'status': 'quivr-search demo UI, from package-lock.json', 'packages': out}


def toolchain():
    go = os.environ.get('GO', 'go')
    try:
        go_version = subprocess.run([go, 'version'], capture_output=True, text=True, check=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        go_version = 'unavailable'
    return {'go': go_version, 'go_mod': re.search(r'^go (\S+)', (ROOT / 'go.mod').read_text(), re.M).group(1),
            'python': platform.python_version(), 'host': f'{platform.system()}/{platform.machine()}'}


def pins():
    """Compact pin manifest for report.json."""
    lock = json.loads((ROOT / 'third_party/e5/model-lock.json').read_text())
    return {'platform': PLATFORM, 'unsupported_platforms': UNSUPPORTED, 'images': [i['image'] for i in images()],
            'model_revision': lock['model_revision'], 'tokenizer': tokenizer()['python_requirements'], 'toolchain': toolchain()}


def inventory(binary=None):
    return {'scope': 'Artifacts built or run by make verify; evaluation stage, no production or legal certification.',
            'platform': PLATFORM, 'unsupported_platforms': UNSUPPORTED, 'toolchain': toolchain(),
            'go_binary': go_modules(binary), 'images': images(), 'model': model(), 'tokenizer': tokenizer(),
            'npm': npm_packages(), 'notices_index': 'third_party/README.md'}


if __name__ == '__main__':
    binary = sys.argv[1] if len(sys.argv) > 1 else None
    data = json.dumps(inventory(binary), indent=2)
    if len(sys.argv) > 2:
        pathlib.Path(sys.argv[2]).write_text(data)
    else:
        print(data)
