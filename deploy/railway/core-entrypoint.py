"""Translate Railway runtime variables into the existing core configuration."""
import json
import os
import pathlib
import subprocess
import sys


def main():
    mode = os.environ.get('QUIVR_ROLE', 'api')
    if mode not in ('api', 'worker', 'migrate'):
        raise SystemExit('QUIVR_ROLE must be api, worker or migrate')
    key = os.environ['QUIVR_API_KEY']
    config = {
        'database_url': os.environ['DATABASE_URL'],
        'cursor_key': os.environ['QUIVR_CURSOR_KEY'],
        'credential_key': os.environ['QUIVR_CREDENTIAL_KEY'],
        'listen': '0.0.0.0:8080',
        'probe_listen': '0.0.0.0:' + os.environ.get('PORT', '8081'),
        'temporal_address': os.environ['TEMPORAL_ADDRESS'],
        'weaviate_url': os.environ['WEAVIATE_URL'],
        'tei_url': os.environ['TEI_URL'],
        'tokenizer': {'python': '/app/.scratch/tokenizer/venv/bin/python',
                      'script': '/app/scripts/token_offsets.py',
                      'model': '/app/.scratch/tokenizer/tokenizer.json'},
        's3': {'endpoint': os.environ['S3_ENDPOINT'], 'access_key': os.environ['S3_ACCESS_KEY'],
               'secret_key': os.environ['S3_SECRET_KEY'], 'bucket': 'quivr-content'},
        'keys': {key: {'organization': 'quivr-demo',
                       'actions': ['corpora:read', 'corpora:write', 'content:read', 'content:write', 'search:query'],
                       'corpora': ['*']}},
    }
    os.umask(0o077)
    path = pathlib.Path('/tmp/quivr-runtime.json')
    path.write_text(json.dumps(config))
    os.environ['QUIVR_CONFIG'] = str(path)
    # Only the API applies startup migrations; failures abort before serving.
    if mode == 'api':
        subprocess.run(['quivr', 'migrate'], check=True)
    os.execvp('quivr', ['quivr', mode])


if __name__ == '__main__':
    try:
        main()
    except KeyError as error:
        sys.exit('Missing runtime variable: ' + str(error))
