"""Start a generic PostgreSQL-backed MLflow service with proxied volume artifacts."""
import configparser
import os
import pathlib
import tempfile


def main():
    required = ('DATABASE_URL', 'MLFLOW_AUTH_DATABASE_URI', 'MLFLOW_AUTH_ADMIN_PASSWORD',
                'MLFLOW_FLASK_SERVER_SECRET_KEY', 'MLFLOW_ALLOWED_HOSTS', 'MLFLOW_CORS_ALLOWED_ORIGINS')
    missing = [k for k in required if not os.environ.get(k, '').strip()]
    if missing:
        raise SystemExit('MLflow: missing required deployment environment variables: ' + ', '.join(missing))
    os.environ.update(MLFLOW_DISABLE_TELEMETRY='true', DO_NOT_TRACK='true', MLFLOW_DISABLE_AGENT_HINT='1')
    def postgres(uri):
        if uri.startswith('postgres://'):
            uri = 'postgresql://' + uri[len('postgres://'):]
        return uri.replace('postgresql://', 'postgresql+psycopg://', 1)
    database = postgres(os.environ['DATABASE_URL'])
    auth_database = postgres(os.environ['MLFLOW_AUTH_DATABASE_URI'])
    if database == auth_database:
        raise SystemExit('MLflow: tracking and authentication need separate databases')
    config = configparser.ConfigParser(interpolation=None)
    config['mlflow'] = {'default_permission': 'NO_PERMISSIONS', 'database_uri': auth_database,
                        'admin_username': os.environ.get('MLFLOW_AUTH_ADMIN_USERNAME', 'admin'),
                        'admin_password': os.environ['MLFLOW_AUTH_ADMIN_PASSWORD'],
                        'authorization_function': 'mlflow.server.auth:authenticate_request_basic_auth',
                        'auth_cache_ttl_seconds': '60'}
    # MLflow reads with ConfigParser interpolation; escape literal percent signs.
    for key, value in list(config['mlflow'].items()):
        config['mlflow'][key] = value.replace('%', '%%')
    fd, path = tempfile.mkstemp(prefix='mlflow-auth-', suffix='.ini')
    with os.fdopen(fd, 'w') as output:
        config.write(output)
    os.environ['MLFLOW_AUTH_CONFIG_PATH'] = path
    artifacts = pathlib.Path(os.environ.get('MLFLOW_ARTIFACTS_DESTINATION', '/data/artifacts'))
    artifacts.mkdir(parents=True, exist_ok=True)
    command = ['mlflow', 'server', '--app-name', 'basic-auth', '--backend-store-uri', database,
               '--serve-artifacts', '--artifacts-destination', str(artifacts), '--host', '0.0.0.0',
               '--port', os.environ.get('PORT', '5000'), '--workers', os.environ.get('MLFLOW_WORKERS', '2'),
               '--allowed-hosts', os.environ['MLFLOW_ALLOWED_HOSTS'], '--x-frame-options', 'DENY']
    # Browser POSTs need full origins (scheme, host and optional port), even
    # when the hostname already appears in --allowed-hosts.
    command += ['--cors-allowed-origins', os.environ['MLFLOW_CORS_ALLOWED_ORIGINS']]
    os.execvp(command[0], command)


if __name__ == '__main__':
    main()
