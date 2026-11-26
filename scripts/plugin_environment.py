"""Keep engine credentials out of local plugin processes."""
import os


ENGINE_SECRETS = {
    'DATABASE_URL', 'S3_ACCESS_KEY', 'S3_SECRET_KEY', 'WEAVIATE_API_KEY',
    'QUIVR_API_KEYS', 'QUIVR_API_KEY', 'QUIVR_ADMIN_KEYS', 'QUIVR_ADMIN_KEY',
    'QUIVR_DEMO_ADMIN_KEY', 'QUIVR_OPERATOR_KEY', 'QUIVR_QUEUE_KEY',
    'QUIVR_CURSOR_KEY', 'QUIVR_CREDENTIAL_KEY',
    'QUIVR_TELEMETRY_HEADERS', 'QUIVR_ENGINE_PLUGIN_KEYS', 'QUIVR_PLUGIN_SIGNING_KEYS',
}


def inherited():
    """Inherit ordinary settings. Callers add only the secrets their plugin uses."""
    env = {}
    for key, value in os.environ.items():
        name = key.upper().removesuffix('_FILE')
        if name not in ENGINE_SECRETS and not name.startswith('QUIVR_LOCAL_WEBHOOK_'):
            env[key] = value
    return env
