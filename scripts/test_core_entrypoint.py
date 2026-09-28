"""The Railway core entrypoint passes credential_key only when it is configured."""
import importlib.util
import pathlib
import unittest

ENTRYPOINT = pathlib.Path(__file__).resolve().parent.parent / 'deploy' / 'railway' / 'core-entrypoint.py'
spec = importlib.util.spec_from_file_location('core_entrypoint', ENTRYPOINT)
core_entrypoint = importlib.util.module_from_spec(spec)
spec.loader.exec_module(core_entrypoint)

# Obvious placeholder values; no real deployment variable is read.
ENV = {'QUIVR_API_KEY': 'placeholder-api-key', 'DATABASE_URL': 'postgres://placeholder',
       'QUIVR_CURSOR_KEY': 'placeholder-cursor-key', 'TEMPORAL_ADDRESS': 'temporal:7233',
       'WEAVIATE_URL': 'http://weaviate', 'TEI_URL': 'http://tei', 'S3_ENDPOINT': 'http://s3',
       'S3_ACCESS_KEY': 'placeholder-access', 'S3_SECRET_KEY': 'placeholder-secret'}


class CoreEntrypointTest(unittest.TestCase):
    def test_credential_key_is_passed_when_set(self):
        config = core_entrypoint.build_config({**ENV, 'QUIVR_CREDENTIAL_KEY': 'placeholder-credential-key'})
        self.assertEqual(config['credential_key'], 'placeholder-credential-key')

    def test_credential_key_is_omitted_when_unset_or_empty(self):
        for env in (ENV, {**ENV, 'QUIVR_CREDENTIAL_KEY': ''}):
            config = core_entrypoint.build_config(env)
            self.assertNotIn('credential_key', config)
            self.assertEqual(config['cursor_key'], 'placeholder-cursor-key')

    def test_required_variables_still_fail_fast(self):
        env = dict(ENV)
        del env['QUIVR_CURSOR_KEY']
        with self.assertRaises(KeyError):
            core_entrypoint.build_config(env)


if __name__ == '__main__':
    unittest.main()
