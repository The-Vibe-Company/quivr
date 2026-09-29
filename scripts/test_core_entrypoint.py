"""The Railway core entrypoint: credential_key only when configured, connector and alert features only on opt-in,
and a worker that runs its plugin sidecars and stops with them."""
import base64
import importlib.util
import pathlib
import re
import sys
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
ENTRYPOINT = ROOT / 'deploy' / 'railway' / 'core-entrypoint.py'
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

    def test_connector_permissions_are_opt_in(self):
        base = core_entrypoint.build_config(ENV)['keys']['placeholder-api-key']['actions']
        self.assertFalse({'connectors:read', 'connectors:write'} & set(base))
        self.assertIn('changes:read', base)
        for value in ('', '0', 'true'):
            actions = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_CONNECTORS': value})['keys']['placeholder-api-key']['actions']
            self.assertEqual(actions, base, value)
        enabled = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_CONNECTORS': '1'})['keys']['placeholder-api-key']['actions']
        self.assertEqual(set(enabled) - set(base), {'connectors:read', 'connectors:write'})

    def test_required_variables_still_fail_fast(self):
        env = dict(ENV)
        del env['QUIVR_CURSOR_KEY']
        with self.assertRaises(KeyError):
            core_entrypoint.build_config(env)


    def test_plugins_are_opt_in(self):
        base = core_entrypoint.build_config(ENV)
        for value in ('', '0', 'true'):
            config = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_PLUGINS': value})
            self.assertEqual(config, base, value)
        self.assertNotIn('destinations', base)
        enabled = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_PLUGINS': '1'})
        actions = enabled['keys']['placeholder-api-key']['actions']
        self.assertEqual(set(actions) - set(base['keys']['placeholder-api-key']['actions']), {'monitoring:read', 'monitoring:write'})
        self.assertEqual(list(enabled['destinations']), [core_entrypoint.DESTINATION_ID])
        self.assertEqual(enabled['plugins'][:len(base['plugins'])], base['plugins'])

    def test_connector_plugins_are_pinned_and_run_without_any_flag(self):
        # Instances of their kinds keep polling once created: an unpinned kind would fail them.
        for env in (ENV, {**ENV, 'QUIVR_DEMO_PLUGINS': '1'}):
            pins = core_entrypoint.build_config(env)['plugins']
            connectors = [p for p in pins if p['endpoint'] in ('http://127.0.0.1:9920', 'http://127.0.0.1:9930')]
            self.assertEqual(connectors, [{'manifest': '/app/plugins/rss/quivr-plugin.yaml', 'endpoint': 'http://127.0.0.1:9920', 'configuration': {}},
                                          {'manifest': '/app/plugins/x-list/quivr-plugin.yaml', 'endpoint': 'http://127.0.0.1:9930', 'configuration': {}}])
        commands = {name: argv for name, argv, _, _ in core_entrypoint.sidecar_commands({**ENV, 'PATH': '/usr/bin'})}
        self.assertEqual(commands, {'rss': ['/usr/local/bin/quivr-rss'], 'x-list': ['/usr/local/bin/quivr-x-list'], 'm365-mail': ['/usr/local/bin/quivr-m365-mail']})
        both = {name for name, *_ in core_entrypoint.sidecar_commands({**ENV, 'QUIVR_DEMO_PLUGINS': '1', 'PATH': '/usr/bin'})}
        self.assertEqual(both, {'rss', 'x-list', 'm365-mail', 'pdf-text', 'alerts'})

    def test_pinned_manifests_are_the_repository_plugins(self):
        # The pins name image paths; each must be a first-party plugin the image copies, with the same id.
        dockerfile = (ROOT / 'deploy' / 'railway' / 'core.Dockerfile').read_text()
        pins = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_PLUGINS': '1'})['plugins']
        self.assertEqual(sorted(p['endpoint'] for p in pins), ['http://127.0.0.1:9900', 'http://127.0.0.1:9910', 'http://127.0.0.1:9920', 'http://127.0.0.1:9930', 'http://127.0.0.1:9940'])
        ids = set()
        for pin in pins:
            source = pathlib.PurePosixPath(pin['manifest']).relative_to('/app')
            if (ROOT / source.parent / 'go.mod').exists():
                # Go connector plugins: the connectors stage builds every plugins/<id> with a go.mod.
                self.assertIn('COPY plugins ./plugins', dockerfile)
            else:
                self.assertIn(f'COPY {source.parent} /app/{source.parent}', dockerfile)
            ids.add(re.search(r'^id: (\S+)$', (ROOT / source).read_text(), re.M).group(1))
        self.assertEqual(ids, {'alerts', 'pdf-text', 'connector.rss', 'connector.x_list', 'connector.m365_mail'})
        pdf = next(p for p in pins if 'pdf-text' in p['manifest'])
        self.assertEqual(pdf['routes'], [{'media_type': 'application/pdf', 'mode': 'required'}])

    def test_sink_destination_passes_the_core_rules_without_private_allowance(self):
        config = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_PLUGINS': '1'})
        self.assertNotIn('delivery', config)
        sink = config['destinations'][core_entrypoint.DESTINATION_ID]
        self.assertEqual(sink['organization'], config['keys']['placeholder-api-key']['organization'])
        # The core refuses a literal private address or a localhost name at startup.
        self.assertTrue(re.fullmatch(r'https?://[a-z0-9.-]+\.invalid/.*', sink['url']), sink['url'])
        # monitoring.ParseSecret: whsec_ and 24 to 64 decoded bytes.
        self.assertTrue(sink['secret'].startswith('whsec_'))
        self.assertTrue(24 <= len(base64.b64decode(sink['secret'][6:], validate=True)) <= 64)
        # api and worker derive the same secret from the shared cursor key; another key gives another secret.
        again = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_PLUGINS': '1'})['destinations']
        self.assertEqual(again[core_entrypoint.DESTINATION_ID]['secret'], sink['secret'])
        other = core_entrypoint.sink_secret('another-placeholder-cursor-key')
        self.assertNotEqual(other, sink['secret'])

    def test_sidecar_environment_carries_only_its_own_secret(self):
        env = {**ENV, 'QUIVR_DEMO_PLUGINS': '1', 'PATH': '/usr/bin', 'TYPESAFE_API_KEY': 'placeholder-typesafe-key'}
        children = {name: child for name, _, _, child in core_entrypoint.sidecar_commands(env)}
        for child in children.values():
            self.assertFalse(set(ENV.values()) & set(child.values()), child)
            self.assertEqual(child['QUIVR_PLUGIN_HOST'], '127.0.0.1')
        self.assertEqual(children['alerts']['TYPESAFE_API_KEY'], 'placeholder-typesafe-key')
        self.assertNotIn('TYPESAFE_API_KEY', children['pdf-text'])
        for value in ('', '  '):
            children = {name: child for name, _, _, child in core_entrypoint.sidecar_commands({**env, 'TYPESAFE_API_KEY': value})}
            self.assertNotIn('TYPESAFE_API_KEY', children['alerts'])

    def test_described_alerts_are_offered_only_with_a_typesafe_key(self):
        def kinds(env):
            pins = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_PLUGINS': '1', **env})['plugins']
            return {pathlib.PurePosixPath(p['manifest']).parent.name: p.get('kinds') for p in pins}
        self.assertEqual(kinds({}), {'alerts': ['keywords'], 'pdf-text': None, 'm365-mail': None, 'rss': None, 'x-list': None})
        self.assertEqual(kinds({'TYPESAFE_API_KEY': ' '}), {'alerts': ['keywords'], 'pdf-text': None, 'm365-mail': None, 'rss': None, 'x-list': None})
        self.assertEqual(kinds({'TYPESAFE_API_KEY': 'placeholder-typesafe-key'})['alerts'], ['keywords', 'described'])


class SuperviseTest(unittest.TestCase):
    def command(self, name, code):
        return (name, [sys.executable, '-c', code], None, None)

    def test_first_exit_stops_the_others_with_its_status(self):
        status = core_entrypoint.supervise([self.command('long', 'import time; time.sleep(60)'),
                                            self.command('failing', 'import sys; sys.exit(3)')], poll=0.01)
        self.assertEqual(status, 3)

    def test_clean_exit_still_restarts_the_container(self):
        status = core_entrypoint.supervise([self.command('long', 'import time; time.sleep(60)'),
                                            self.command('done', 'pass')], poll=0.01)
        self.assertEqual(status, 1)

    def test_sigterm_stops_every_process_and_exits_cleanly(self):
        # The child asks for the stop itself, so no timer is involved.
        stopper = 'import os, signal, time; os.kill(os.getppid(), signal.SIGTERM); time.sleep(60)'
        status = core_entrypoint.supervise([self.command('long', 'import time; time.sleep(60)'),
                                            self.command('stopper', stopper)], poll=0.01)
        self.assertEqual(status, 0)


if __name__ == '__main__':
    unittest.main()
