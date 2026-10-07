"""The Railway core entrypoint: credential_key only when configured, connector and alert features only on opt-in,
and a worker that runs its plugin sidecars and stops with them."""
import base64
import importlib.util
import json
import os
import pathlib
import re
import sys
import tempfile
import unittest
from unittest.mock import patch

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
    def test_core_processes_keep_runtime_config_without_provider_secret(self):
        # main owns environment inheritance; sidecar_commands alone cannot see this leak.
        with tempfile.TemporaryDirectory() as tmp:
            runtime = pathlib.Path(tmp) / 'runtime.json'
            manifest = pathlib.Path(tmp) / 'hosted-embed' / 'quivr-plugin.yaml'
            for role, provider in ((r, p) for r in ('api', 'worker', 'migrate') for p in ('cohere', 'gemma')):
                with self.subTest(role=role, provider=provider), patch.dict(os.environ, {
                    **ENV, 'QUIVR_ROLE': role, 'QUIVR_DEMO_JEV_RERANK': '1',
                    'TYPESAFE_API_KEY': 'fixture-typesafe-key', 'PATH': '/usr/bin',
                    'AZURE_FOUNDRY_KEY': 'fixture-foundry-key', 'QUIVR_DEMO_EMBEDDING': provider,
                    'EMBED_URL': 'https://example--embeddings.modal.run', 'EMBED_API_KEY': 'fixture-modal-token',
                    'AZURE_FOUNDRY_ENDPOINT': 'https://resource.example.org', 'QUIVR_DEMO_HOSTED_EMBED': '1',
                }, clear=True), patch.object(core_entrypoint.pathlib, 'Path',
                    side_effect=lambda value: runtime if value == '/tmp/quivr-runtime.json' else manifest), \
                    patch.object(core_entrypoint.os, 'umask'), \
                    patch.object(core_entrypoint.subprocess, 'run') as migrate, \
                    patch.object(core_entrypoint, 'supervise', return_value=0) as supervise, \
                    patch.object(core_entrypoint.os, 'execvp', side_effect=SystemExit(0)) as inherited_exec, \
                    patch.object(core_entrypoint.os, 'execvpe', side_effect=SystemExit(0)) as explicit_exec:
                    with self.assertRaises(SystemExit) as stopped:
                        core_entrypoint.main()
                    self.assertEqual(stopped.exception.code, 0)
                    configure = migrate.call_args_list[0]
                    self.assertEqual(configure.args[0], ['/usr/local/bin/quivr-hosted-embed', 'configure',
                                     str(manifest.parent / 'configuration.json')])
                    self.assertNotIn('AZURE_FOUNDRY_KEY', configure.kwargs['env'])
                    self.assertNotIn('EMBED_API_KEY', configure.kwargs['env'])
                    self.assertNotIn('fixture-modal-token', (manifest.parent / 'configuration.json').read_text())
                    self.assertNotIn('fixture-foundry-key', (manifest.parent / 'configuration.json').read_text())
                    if role == 'migrate':
                        effective = explicit_exec.call_args.args[2] if explicit_exec.called else dict(os.environ)
                    else:
                        commands = supervise.call_args.args[0]
                        core_env = next(child for name, _, _, child in commands if name == 'quivr ' + role)
                        effective = core_env if core_env is not None else dict(os.environ)
                        jev = [child for name, _, _, child in commands if name == 'jev-rerank']
                        hosted = [child for name, _, _, child in commands if name == 'hosted-embed']
                        self.assertEqual(hosted[0]['AZURE_FOUNDRY_KEY'],
                                         'fixture-foundry-key' if provider == 'cohere' else 'fixture-modal-token')
                        self.assertEqual(len(jev), 1 if role == 'api' else 0)
                        if jev:
                            self.assertEqual(jev[0]['TYPESAFE_API_KEY'], 'fixture-typesafe-key')
                    self.assertNotIn('TYPESAFE_API_KEY', effective)
                    self.assertNotIn('AZURE_FOUNDRY_KEY', effective)
                    self.assertNotIn('EMBED_API_KEY', effective)
                    self.assertEqual(effective['QUIVR_CONFIG'], str(runtime))
                    self.assertEqual(effective['DATABASE_URL'], ENV['DATABASE_URL'])
                    if role == 'api':
                        migration_env = migrate.call_args.kwargs.get('env', dict(os.environ))
                        self.assertNotIn('TYPESAFE_API_KEY', migration_env)
                        self.assertNotIn('AZURE_FOUNDRY_KEY', migration_env)
                        self.assertNotIn('EMBED_API_KEY', migration_env)
                        self.assertEqual(migration_env['QUIVR_CONFIG'], str(runtime))
                    else:
                        self.assertEqual(migrate.call_count, 1)  # configure only; no migration

    def test_jev_adds_deep_beside_normal_search_and_runs_only_on_api(self):
        # Owns the deployment switch, process selection and secret boundary; no provider call.
        for switch, key, enabled in [('', 'fixture-typesafe-key', False), ('0', 'fixture-typesafe-key', False),
                                     ('true', 'fixture-typesafe-key', False), ('1', '', True),
                                     ('1', '  ', True), ('1', 'fixture-typesafe-key', True)]:
            with self.subTest(switch=switch, key_present=bool(key.strip())):
                env = {**ENV, 'QUIVR_DEMO_JEV_RERANK': switch, 'TYPESAFE_API_KEY': key}
                config = core_entrypoint.build_config(env)
                pins = {pathlib.PurePosixPath(p['manifest']).parent.name: p for p in config['plugins']}
                self.assertIn('core-retrieve', pins)
                self.assertEqual('jev-rerank' in pins, enabled)
                self.assertNotIn('TYPESAFE_API_KEY', json.dumps(config))
                if key.strip():
                    self.assertNotIn(key, json.dumps(config))
                api = {name: (argv, cwd, child) for name, argv, cwd, child in core_entrypoint.sidecar_commands(env, 'api')}
                self.assertIn('core-retrieve', api)
                self.assertEqual('jev-rerank' in api, enabled)
                self.assertEqual(api['core-retrieve'][2]['QUIVR_PLUGIN_PORT'], '9960')
                worker = {name: child for name, _, _, child in core_entrypoint.sidecar_commands(env, 'worker')}
                self.assertFalse({'jev-rerank', 'core-retrieve'} & worker.keys())
                for name, (_, _, child) in api.items():
                    if name != 'jev-rerank':
                        self.assertNotIn('TYPESAFE_API_KEY', child)
                if enabled:
                    self.assertEqual(config['retrieval']['profiles'], {
                        'default': 'core.retrieve/default', 'deep': 'jev.rerank/deep'})
                    self.assertEqual(len({p['endpoint'] for p in pins.values()}), len(pins))
                    self.assertEqual(pins['jev-rerank'], {
                        'manifest': '/app/plugins/jev-rerank/quivr-plugin.yaml',
                        'endpoint': 'http://127.0.0.1:9970',
                        'configuration': {'candidate_count': 30, 'trim_tokens': '256',
                                          'tokenizer_path': '/app/.scratch/tokenizer/tokenizer.json',
                                          'ranking': 'noul', 'cache_entries': 4096}})
                    argv, cwd, child = api['jev-rerank']
                    self.assertEqual(argv, ['/opt/quivr-plugins/bin/python', '-m', 'jev_rerank'])
                    self.assertEqual(cwd, '/app/plugins/jev-rerank')
                    self.assertEqual(child.get('TYPESAFE_API_KEY'), key if key.strip() else None)
                    self.assertEqual(child['QUIVR_PLUGIN_PORT'], '9970')
                    self.assertEqual(child['QUIVR_PLUGIN_MANIFEST'], pins['jev-rerank']['manifest'])
                    self.assertFalse(set(ENV.values()) & set(child.values()))
                else:
                    self.assertEqual(config, core_entrypoint.build_config(ENV))

    def test_hosted_embedding_is_default_for_all_sources_and_has_its_own_secret(self):
        # Owns runtime selection and inheritance; a shared-key or served-owner
        # regression is not visible to the hosted plugin's provider tests.
        for switch, selection in [(None, None), ('', None), ('0', None), ('true', None), ('1', None),
                                  (None, 'cohere'), ('1', 'cohere')]:
            with self.subTest(switch=switch, selection=selection):
                env = {**ENV, 'QUIVR_DEMO_HOSTED_EMBED': switch,
                       'AZURE_FOUNDRY_ENDPOINT': 'https://resource.example.org/',
                       'AZURE_FOUNDRY_KEY': 'fixture-foundry-key'}
                if switch is None:
                    del env['QUIVR_DEMO_HOSTED_EMBED']
                if selection is not None:
                    env['QUIVR_DEMO_EMBEDDING'] = selection
                config = core_entrypoint.build_config(env)
                pins = {pathlib.PurePosixPath(p['manifest']).parent.name: p for p in config['plugins']}
                enabled = switch == '1' or selection == 'cohere'
                self.assertEqual('hosted-embed' in pins, enabled)
                self.assertEqual(pins['core-ingest'], next(p for p in core_entrypoint.build_config(ENV)['plugins']
                                 if p['manifest'] == pins['core-ingest']['manifest']))
                self.assertNotIn('fixture-foundry-key', json.dumps(config))
                if enabled:
                    # A default without routes covers every normalized source,
                    # including XML and future media types; no E5 evaluation.
                    self.assertEqual(config['ingestion'], {'default': 'hosted.embed'})
                    hosted = pins['hosted-embed']['configuration']
                    self.assertEqual(hosted['base_url'], 'https://resource.example.org/providers/cohere/v2')
                    self.assertEqual((hosted['model'], hosted['dimensions']), ('Cohere-Embed-V5-Pro', 1024))
                    self.assertEqual((hosted['document_input_type'], hosted['query_input_type']),
                                     ('search_document', 'search_query'))
                    self.assertEqual(hosted['usd_per_million_tokens'], 0.12)
                else:
                    self.assertEqual(config, core_entrypoint.build_config(ENV))
                for role in ('api', 'worker'):
                    children = {name: (argv, child) for name, argv, _, child in core_entrypoint.sidecar_commands(env, role)}
                    self.assertEqual('hosted-embed' in children, enabled)
                    for name, (argv, child) in children.items():
                        if name == 'hosted-embed':
                            self.assertEqual(argv, ['/usr/local/bin/quivr-hosted-embed'])
                            self.assertEqual(child['AZURE_FOUNDRY_KEY'], 'fixture-foundry-key')
                            self.assertEqual(child['QUIVR_PLUGIN_MANIFEST'], pins['hosted-embed']['manifest'])
                            self.assertEqual(child['QUIVR_PLUGIN_PORT'], '9980')
                            self.assertNotIn('AZURE_FOUNDRY_ENDPOINT', child)
                        else:
                            self.assertNotIn('AZURE_FOUNDRY_KEY', child)

    def test_hosted_embedding_requires_endpoint_and_key_only_when_enabled(self):
        for name in ('AZURE_FOUNDRY_ENDPOINT', 'AZURE_FOUNDRY_KEY'):
            for value in ('', '  '):
                env = {**ENV, 'QUIVR_DEMO_HOSTED_EMBED': '1',
                       'AZURE_FOUNDRY_ENDPOINT': 'https://resource.example.org',
                       'AZURE_FOUNDRY_KEY': 'fixture-foundry-key', name: value}
                with self.subTest(name=name, value=value):
                    with self.assertRaisesRegex(ValueError, name):
                        core_entrypoint.build_config(env)

    def test_gemma_selects_modal_openai_embeddings_with_an_isolated_bearer_secret(self):
        # Deployment mapping owns model identity, prompt bytes and opt-in selection.
        env = {**ENV, 'QUIVR_DEMO_EMBEDDING': 'gemma',
               'EMBED_URL': 'https://example--embeddings.modal.run/', 'EMBED_API_KEY': 'fixture-modal-token',
               'AZURE_FOUNDRY_KEY': 'unused-placeholder-key'}
        config = core_entrypoint.build_config(env)
        pin = next(p for p in config['plugins'] if p['manifest'] == '/tmp/hosted-embed/quivr-plugin.yaml')
        self.assertEqual(config['ingestion'], {'default': 'hosted.embed'})
        self.assertEqual(pin['configuration'], {
            'format': 'openai', 'base_url': 'https://example--embeddings.modal.run/v1',
            'auth': 'bearer', 'model': 'google/embeddinggemma-2', 'dimensions': 768,
            'model_revision': '914f7f89142e33e7',
            'query_prefix': 'task: search result | query: ',
            'document_prefix': 'title: none | text: ',
            'max_tokens_per_segment': 2048, 'overlap': 64,
            'batch_size': 32, 'max_batch_tokens': 65536, 'max_concurrent_requests': 4,
            'request_timeout_ms': 10000, 'call_budget_ms': 90000, 'usd_per_million_tokens': 0,
        })
        self.assertNotIn('fixture-modal-token', json.dumps(config))
        for role in ('api', 'worker'):
            child = next(child for name, _, _, child in core_entrypoint.sidecar_commands(env, role)
                         if name == 'hosted-embed')
            self.assertEqual(child['AZURE_FOUNDRY_KEY'], 'fixture-modal-token')
            self.assertNotIn('EMBED_API_KEY', child)
        for selection, endpoint in [('typo', env['EMBED_URL']), ('gemma', ''),
                                    ('gemma', 'http://example--embeddings.modal.run'),
                                    ('gemma', 'https://user:pass@example--embeddings.modal.run'),
                                    ('gemma', 'https://example--embeddings.modal.run/v1?key=value'),
                                    ('gemma', 'https://bad host.modal.run'),
                                    ('gemma', 'https://host.modal.run:not-a-port'),
                                    ('gemma', 'https://host.modal.run:65536')]:
            with self.subTest(selection=selection, endpoint=endpoint):
                with self.assertRaises(ValueError):
                    core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_EMBEDDING': selection,
                                                'EMBED_URL': endpoint, 'EMBED_API_KEY': 'fixture-modal-token'})
        for key in ('', ' ', 'has space', 'has\nnewline', 'non-ascii-é'):
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, 'EMBED_API_KEY'):
                core_entrypoint.build_config({**env, 'EMBED_API_KEY': key})

    def test_credential_key_is_passed_when_set(self):
        config = core_entrypoint.build_config({**ENV, 'QUIVR_CREDENTIAL_KEY': 'placeholder-credential-key'})
        self.assertEqual(config['credential_key'], 'placeholder-credential-key')

    def test_credential_key_is_omitted_when_unset_or_empty(self):
        for env in (ENV, {**ENV, 'QUIVR_CREDENTIAL_KEY': ''}):
            config = core_entrypoint.build_config(env)
            self.assertNotIn('credential_key', config)
            self.assertEqual(config['cursor_key'], 'placeholder-cursor-key')

    def test_demo_records_query_text(self):
        # The engine default is off; the demo's usage view lists its most frequent queries.
        self.assertIs(core_entrypoint.build_config(ENV)['observability']['record_query_text'], True)

    def test_operator_key_is_separate_and_opt_in(self):
        self.assertEqual(len(core_entrypoint.build_config(ENV)['keys']), 1)
        keys = core_entrypoint.build_config({**ENV, 'QUIVR_OPERATOR_KEY': 'placeholder-operator-key'})['keys']
        for action in ('projections:rebuild', 'plugins:admin', 'operations:write', 'queues:read', 'corpora:archive', 'corpora:rename'):
            self.assertIn(action, keys['placeholder-operator-key']['actions'])
        # The web app's key never administers plugins, whatever else is enabled.
        everything = {**ENV, 'QUIVR_DEMO_CONNECTORS': '1', 'QUIVR_DEMO_PLUGINS': '1', 'QUIVR_OPERATOR_KEY': 'placeholder-operator-key'}
        for action in ('projections:rebuild', 'plugins:admin', 'operations:write', 'queues:read', 'corpora:archive', 'corpora:rename'):
            self.assertNotIn(action, core_entrypoint.build_config(everything)['keys'][ENV['QUIVR_API_KEY']]['actions'])

    def test_connector_permissions_are_opt_in(self):
        base = core_entrypoint.build_config(ENV)['keys']['placeholder-api-key']['actions']
        self.assertFalse({'connectors:read', 'connectors:write'} & set(base))
        self.assertIn('changes:read', base)
        for value in ('', '0', 'true'):
            actions = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_CONNECTORS': value})['keys']['placeholder-api-key']['actions']
            self.assertEqual(actions, base, value)
        enabled = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_CONNECTORS': '1'})['keys']['placeholder-api-key']['actions']
        self.assertEqual(set(enabled) - set(base), {'connectors:read', 'connectors:write'})

    def test_admin_read_is_opt_in(self):
        base = core_entrypoint.build_config(ENV)['keys']['placeholder-api-key']['actions']
        for value in ('', '0', 'true'):
            actions = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_ADMIN': value})['keys']['placeholder-api-key']['actions']
            self.assertEqual(actions, base, value)
        enabled = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_ADMIN': '1'})['keys']['placeholder-api-key']['actions']
        self.assertEqual(set(enabled) - set(base), {'observability:read'})
        operator = core_entrypoint.build_config({**ENV, 'QUIVR_OPERATOR_KEY': 'placeholder-operator-key'})['keys']['placeholder-operator-key']
        self.assertIn('observability:read', operator['actions'])

    def test_queue_reader_key_is_read_only_and_separate(self):
        config = core_entrypoint.build_config({**ENV, 'QUIVR_QUEUE_KEY': ' fixture-queue-key '})
        self.assertEqual(config['keys']['fixture-queue-key'], {
            'organization': 'quivr-demo', 'actions': ['queues:read'], 'corpora': ['*']})
        for existing in ('placeholder-api-key', 'placeholder-operator-key'):
            with self.subTest(existing=existing), self.assertRaises(ValueError):
                core_entrypoint.build_config({**ENV, 'QUIVR_OPERATOR_KEY': 'placeholder-operator-key',
                                             'QUIVR_QUEUE_KEY': existing})

    def test_worker_queue_environment_translation(self):
        # Literal external keys and omitted config preserve existing mixed workers.
        self.assertNotIn('worker', core_entrypoint.build_config(ENV))
        for queue in ('live', 'bulk', 'live,bulk'):
            config = core_entrypoint.build_config({**ENV, 'QUIVR_WORKER_QUEUES': queue,
                         'QUIVR_WORKER_LIVE_SLOTS': '3', 'QUIVR_WORKER_BULK_SLOTS': '7'})
            self.assertEqual(config['worker'], {'queues': queue.split(','),
                                               'slots': {'live': 3, 'bulk': 7}})
        for name, values in {
            'QUIVR_WORKER_QUEUES': ('', 'other', 'live,live', 'live,'),
            'QUIVR_WORKER_LIVE_SLOTS': ('0', '-1', '1025', 'many'),
            'QUIVR_WORKER_BULK_SLOTS': ('0', '-1', '1025', 'many'),
        }.items():
            for value in values:
                with self.subTest(name=name, value=value), self.assertRaises(ValueError):
                    core_entrypoint.build_config({**ENV, name: value})

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
        for env in (ENV, *({**ENV, 'QUIVR_DEMO_PLUGINS': value} for value in ('0', '1'))):
            pins = core_entrypoint.build_config(env)['plugins']
            newsml = next(p for p in pins if p['manifest'] == '/app/plugins/newsml-g2/quivr-plugin.yaml')
            self.assertEqual(newsml, {
                'manifest': '/app/plugins/newsml-g2/quivr-plugin.yaml',
                'endpoint': 'http://127.0.0.1:9905', 'configuration': {},
                'routes': [{'media_type': 'application/vnd.iptc.g2.newsitem+xml', 'mode': 'required'},
                           {'media_type': 'application/vnd.iptc.g2.newsmessage+xml', 'mode': 'required'}]})
            self.assertEqual(len({p['endpoint'] for p in pins}), len(pins))
            worker = {name: (argv, cwd, child) for name, argv, cwd, child in core_entrypoint.sidecar_commands(env)}
            argv, cwd, child = worker['newsml-g2']
            self.assertEqual(argv, ['/opt/quivr-plugins/bin/python', '-m', 'newsml_g2'])
            self.assertEqual(cwd, '/app/plugins/newsml-g2')
            self.assertEqual(child['QUIVR_PLUGIN_HOST'], '127.0.0.1')
            self.assertEqual(child['QUIVR_PLUGIN_PORT'], '9905')
            self.assertEqual(child['QUIVR_PLUGIN_MANIFEST'], newsml['manifest'])
            self.assertFalse(set(ENV.values()) & set(child.values()))
            self.assertNotIn('newsml-g2', {name for name, *_ in core_entrypoint.sidecar_commands(env, 'api')})
            connectors = [p for p in pins if p['endpoint'] in ('http://127.0.0.1:9920', 'http://127.0.0.1:9930')]
            self.assertEqual(connectors, [{'manifest': '/app/plugins/rss/quivr-plugin.yaml', 'endpoint': 'http://127.0.0.1:9920', 'configuration': {}},
                                          {'manifest': '/app/plugins/x-list/quivr-plugin.yaml', 'endpoint': 'http://127.0.0.1:9930', 'configuration': {}}])
        commands = {name: argv for name, argv, _, _ in core_entrypoint.sidecar_commands({**ENV, 'PATH': '/usr/bin'})}
        self.assertEqual(commands, {'rss': ['/usr/local/bin/quivr-rss'], 'x-list': ['/usr/local/bin/quivr-x-list'], 'm365-mail': ['/usr/local/bin/quivr-m365-mail'], 'object-storage-archive': ['/usr/local/bin/quivr-object-storage-archive'],
                                    'core-ingest': ['/usr/local/bin/quivr-core-ingest'],
                                    'newsml-g2': ['/opt/quivr-plugins/bin/python', '-m', 'newsml_g2']})
        both = {name for name, *_ in core_entrypoint.sidecar_commands({**ENV, 'QUIVR_DEMO_PLUGINS': '1', 'PATH': '/usr/bin'})}
        self.assertEqual(both, {'rss', 'x-list', 'm365-mail', 'object-storage-archive', 'core-ingest', 'newsml-g2', 'pdf-text', 'alerts'})
        # The API runs only the push connector plugins, to relay webhook deliveries to them,
        # the ingestion plugin, to encode queries, the retrieval plugin, to rank searches,
        # and the alerts plugin, for Subscription previews.
        api = {name for name, *_ in core_entrypoint.sidecar_commands({**ENV, 'QUIVR_DEMO_PLUGINS': '1', 'PATH': '/usr/bin'}, 'api')}
        self.assertEqual(api, {'x-list', 'core-ingest', 'core-retrieve', 'alerts'})
        api = {name for name, *_ in core_entrypoint.sidecar_commands({**ENV, 'PATH': '/usr/bin'}, 'api')}
        self.assertEqual(api, {'x-list', 'core-ingest', 'core-retrieve'})

    def test_core_ingest_embeds_with_the_deployment_tei_and_the_image_tokenizer(self):
        # The engine segments and embeds nothing itself: api and worker refuse to start without it.
        pin = next(p for p in core_entrypoint.build_config(ENV)['plugins'] if p['manifest'] == '/app/plugins/core-ingest/quivr-plugin.yaml')
        self.assertEqual(pin['configuration']['tei_url'], ENV['TEI_URL'])
        self.assertEqual(pin['configuration']['tokenizer'], {'python': '/app/.scratch/tokenizer/venv/bin/python', 'model': '/app/.scratch/tokenizer/tokenizer.json'})
        self.assertNotIn('tokenizer', core_entrypoint.build_config(ENV))

    def test_public_url_is_passed_when_set(self):
        self.assertNotIn('public_url', core_entrypoint.build_config(ENV))
        self.assertNotIn('public_url', core_entrypoint.build_config({**ENV, 'QUIVR_PUBLIC_URL': ' '}))
        config = core_entrypoint.build_config({**ENV, 'QUIVR_PUBLIC_URL': 'https://quivr.example.com'})
        self.assertEqual(config['public_url'], 'https://quivr.example.com')

    def test_rebuild_concurrency_is_passed_when_set(self):
        self.assertNotIn('rebuild', core_entrypoint.build_config(ENV))
        config = core_entrypoint.build_config({**ENV, 'QUIVR_REBUILD_CONCURRENCY': '32'})
        self.assertEqual(config['rebuild'], {'concurrency': 32})
        for bad in ('abc', '0', '33'):
            with self.assertRaisesRegex(ValueError, 'QUIVR_REBUILD_CONCURRENCY'):
                core_entrypoint.build_config({**ENV, 'QUIVR_REBUILD_CONCURRENCY': bad})

    def test_pinned_manifests_are_the_repository_plugins(self):
        # The pins name image paths; each must be a first-party plugin the image copies, with the same id.
        dockerfile = (ROOT / 'deploy' / 'railway' / 'core.Dockerfile').read_text()
        pins = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_PLUGINS': '1'})['plugins']
        self.assertEqual(sorted(p['endpoint'] for p in pins), ['http://127.0.0.1:9900', 'http://127.0.0.1:9905', 'http://127.0.0.1:9910', 'http://127.0.0.1:9920', 'http://127.0.0.1:9930', 'http://127.0.0.1:9940', 'http://127.0.0.1:9950', 'http://127.0.0.1:9960', 'http://127.0.0.1:9990'])
        ids = set()
        for pin in pins:
            source = pathlib.PurePosixPath(pin['manifest']).relative_to('/app')
            if (ROOT / source.parent / 'go.mod').exists():
                # Go connector plugins: the connectors stage builds every plugins/<id> with a go.mod.
                self.assertIn('COPY plugins ./plugins', dockerfile)
            else:
                self.assertIn(f'COPY {source.parent} /app/{source.parent}', dockerfile)
            ids.add(re.search(r'^id: (\S+)$', (ROOT / source).read_text(), re.M).group(1))
        self.assertEqual(ids, {'alerts', 'pdf-text', 'newsml-g2', 'connector.rss', 'connector.x_list', 'connector.m365_mail', 'connector.object_storage_archive', 'core.ingest', 'core.retrieve'})
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

    def test_archive_signing_ring_is_shared_only_with_its_sidecar(self):
        plugin_id = 'connector.object_storage_archive'
        # Literal external key-ring fields protect deployment compatibility.
        ring = {'active': 'primary', 'keys': [{'id': 'primary', 'secret': 'c3ludGhldGljLXNpZ25pbmcta2V5LXdpdGgtMzItYnl0ZXM'}]}
        env = {**ENV, 'QUIVR_ENGINE_PLUGIN_KEYS': json.dumps({plugin_id: ring}),
               'QUIVR_PLUGIN_SIGNING_KEYS': 'unrelated-parent-ring'}
        engine = json.loads(core_entrypoint.engine_signing_environment(env)['QUIVR_ENGINE_PLUGIN_KEYS'])
        children = {name: child for name, _, _, child in core_entrypoint.sidecar_commands(env)}
        self.assertEqual(engine[plugin_id], ring)
        self.assertEqual(json.loads(children['object-storage-archive']['QUIVR_PLUGIN_SIGNING_KEYS']), ring)
        for name, child in children.items():
            self.assertNotIn('QUIVR_ENGINE_PLUGIN_KEYS', child)
            if name != 'object-storage-archive':
                self.assertNotIn('QUIVR_PLUGIN_SIGNING_KEYS', child)
        for ambiguous in ('{"same":{},"same":{}}',
                          '{"other":{"active":"one","active":"two","keys":[]}}'):
            with self.assertRaisesRegex(ValueError, 'Invalid engine plugin signing configuration: duplicate JSON member'):
                core_entrypoint.engine_signing_environment({**ENV, 'QUIVR_ENGINE_PLUGIN_KEYS': ambiguous})
        generated = core_entrypoint.engine_signing_environment(ENV)
        self.assertEqual(generated, core_entrypoint.engine_signing_environment(dict(ENV)))
        derived = json.loads(generated['QUIVR_ENGINE_PLUGIN_KEYS'])[plugin_id]['keys'][0]['secret']
        changed = {**ENV, 'QUIVR_CURSOR_KEY': 'another-synthetic-root-key'}
        self.assertNotEqual(derived, json.loads(core_entrypoint.engine_signing_environment(changed)['QUIVR_ENGINE_PLUGIN_KEYS'])[plugin_id]['keys'][0]['secret'])

    def test_described_alerts_are_offered_only_with_a_typesafe_key(self):
        def kinds(env):
            pins = core_entrypoint.build_config({**ENV, 'QUIVR_DEMO_PLUGINS': '1', **env})['plugins']
            return {pathlib.PurePosixPath(p['manifest']).parent.name: p.get('kinds') for p in pins}
        others = {'object-storage-archive': None, 'newsml-g2': None, 'pdf-text': None, 'm365-mail': None, 'rss': None, 'x-list': None, 'core-ingest': None, 'core-retrieve': None}
        self.assertEqual(kinds({}), {'alerts': ['keywords'], **others})
        self.assertEqual(kinds({'TYPESAFE_API_KEY': ' '}), {'alerts': ['keywords'], **others})
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
