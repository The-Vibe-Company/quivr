"""Private secret files for the development and load-test engines."""
import json
import os
from pathlib import Path
import tempfile


def split_config(directory, config, *, keyless=False):
    """Move credentials out of a freshly built config before it is serialized."""
    root = directory / ('keyless-secrets' if keyless else 'secrets')
    root.mkdir(mode=0o700, exist_ok=True)
    bindings = {
        'DATABASE_URL': config.pop('database_url'),
        'QUIVR_CURSOR_KEY': config.pop('cursor_key'),
        'QUIVR_API_KEYS': json.dumps(config.pop('keys')),
        'S3_ACCESS_KEY': config['s3'].pop('access_key'),
        'S3_SECRET_KEY': config['s3'].pop('secret_key'),
    }
    credential_key = config.pop('credential_key', '')
    if credential_key:
        bindings['QUIVR_CREDENTIAL_KEY'] = credential_key
    for index, destination in enumerate(config.get('destinations', {}).values()):
        if 'secret' in destination:
            name = f'QUIVR_LOCAL_WEBHOOK_{index}'
            bindings[name] = destination.pop('secret')
            destination['secret_env'] = name
    for name, value in bindings.items():
        # Replace atomically so readers never see a partial key or scope map.
        with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8', dir=root, delete=False) as output:
            tmp = Path(output.name)
            try:
                output.write(value)
                output.close()
                tmp.replace(root / name)
            finally:
                tmp.unlink(missing_ok=True)
    return config


def engine_environment(config, environ=None):
    """Bind local credentials to the specific config and preserve other settings."""
    config = Path(config)
    keyless = config.name in ('keyless.json', 'keyless-worker.json')
    root = config.parent / ('keyless-secrets' if keyless else 'secrets')
    env = dict(os.environ if environ is None else environ)
    if root.is_dir():
        if keyless:
            env.pop('QUIVR_CREDENTIAL_KEY', None)
            env.pop('QUIVR_CREDENTIAL_KEY_FILE', None)
        for path in root.iterdir():
            if not path.name.isupper() or not path.is_file():
                continue
            env.pop(path.name, None)
            env[path.name + '_FILE'] = str(path.resolve())
    return {**env, 'QUIVR_CONFIG': str(config)}


def read_secret(env, name, fallback=None):
    value, path = env.get(name), env.get(name + '_FILE')
    if not path:
        return value or fallback
    if value:
        raise ValueError('Set either ' + name + ' or ' + name + '_FILE')
    try:
        value = Path(path).read_bytes().decode('utf-8')
    except (OSError, ValueError):
        raise ValueError('Cannot read ' + name + '_FILE') from None
    if not value.strip() or '\0' in value:
        raise ValueError(name + '_FILE must contain a nonempty secret')
    return value.removesuffix('\r\n') if value.endswith('\r\n') else value.removesuffix('\n')


def read_dependencies(path):
    """Reconstruct the dependency settings in memory for local verification tools."""
    path = Path(path)
    cfg = json.loads(path.read_text())
    env = engine_environment(path)
    for field, name in (('database_url', 'DATABASE_URL'), ('weaviate_api_key', 'WEAVIATE_API_KEY')):
        value = read_secret(env, name, cfg.get(field))
        if value is not None:
            cfg[field] = value
    for field, name in (('access_key', 'S3_ACCESS_KEY'), ('secret_key', 'S3_SECRET_KEY')):
        if 's3' in cfg:
            value = read_secret(env, name, cfg['s3'].get(field))
            if value is not None:
                cfg['s3'][field] = value
    return cfg
