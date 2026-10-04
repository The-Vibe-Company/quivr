"""Deployment boundary: browser origins are required and reach MLflow unchanged.

No existing startup test owns this contract. Omitting or dropping the setting
causes browser POSTs to fail with 403 despite valid authentication.
"""
import importlib.util
import os
import pathlib
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location(
    'mlflow_entrypoint', pathlib.Path(__file__).resolve().parents[1] / 'deploy/mlflow/entrypoint.py')
entrypoint = importlib.util.module_from_spec(spec)
spec.loader.exec_module(entrypoint)


class BrowserOrigins(unittest.TestCase):
    def test_startup_requires_origins_and_forwards_explicit_scheme_host_and_port(self):
        env = {'DATABASE_URL': 'postgresql://example/tracking',
               'MLFLOW_AUTH_DATABASE_URI': 'postgresql://example/auth',
               'MLFLOW_AUTH_ADMIN_PASSWORD': 'example-password',
               'MLFLOW_FLASK_SERVER_SECRET_KEY': 'example-secret',
               'MLFLOW_ALLOWED_HOSTS': 'results.example.com,localhost:5000'}
        with tempfile.TemporaryDirectory() as temp:
            env['MLFLOW_ARTIFACTS_DESTINATION'] = temp
            for origins in (None, '', ' '):
                with self.subTest(origins=origins), mock.patch.dict(os.environ, env, clear=True), \
                        mock.patch.object(tempfile, 'tempdir', temp), \
                        mock.patch.object(os, 'execvp', side_effect=SystemExit('unexpected server startup')):
                    if origins is not None:
                        os.environ['MLFLOW_CORS_ALLOWED_ORIGINS'] = origins
                    with self.assertRaisesRegex(SystemExit, 'MLFLOW_CORS_ALLOWED_ORIGINS'):
                        entrypoint.main()
            origins = 'https://results.example.com,http://localhost:5000'
            with mock.patch.dict(os.environ, {**env, 'MLFLOW_CORS_ALLOWED_ORIGINS': origins,
                    'MLFLOW_ARTIFACTS_DESTINATION': temp}, clear=True), \
                    mock.patch.object(tempfile, 'tempdir', temp), \
                    mock.patch.object(os, 'execvp', side_effect=SystemExit(0)) as execute:
                with self.assertRaises(SystemExit) as exit:
                    entrypoint.main()
                self.assertEqual(exit.exception.code, 0)
                command = execute.call_args.args[1]
                self.assertEqual(command[command.index('--cors-allowed-origins') + 1], origins)


if __name__ == '__main__':
    unittest.main()
