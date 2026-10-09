"""Deployment configuration contracts at the operator tooling boundary."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import subprocess
import sys
import unittest
from unittest.mock import Mock, patch


class RailwayTransport:
    """Fixed provider responses; only the real adapter decides writes and drift."""
    def __init__(self):
        self.calls = []
        self.variables = {"ASYNC_INDEXING": "false", "PROVIDER_KEY": "private"}
        self.startup = "ASYNC_INDEXING=false\n"
        self.deployment_status = "SUCCESS"
        self.image = "registry/image@sha256:" + "a" * 64
        self.budgets = {"containers": {"memoryBytes": 2000000000, "cpu": 2}}
        self.acknowledge = True
        self.instances = [{"id": "instance", "status": "RUNNING"}]
        self.dockerfile = None

    def __call__(self, args, stdin=None):
        self.calls.append((args, stdin))
        if args[0] == "api":
            if args[1].startswith('mutation'):
                variables = json.loads(args[3])
                if 'memoryGB' in variables.get('input', {}):
                    self.budgets = {'containers': {'memoryBytes': variables['input']['memoryGB'] * 1e9,
                                                  'cpu': variables['input']['vCPUs']}}
                    return json.dumps({'data': {'serviceInstanceLimitsUpdate': self.acknowledge}})
                if 'source' in variables['input']:
                    self.image = variables['input']['source']['image']
                self.dockerfile = variables['input'].get('dockerfilePath', self.dockerfile)
                return json.dumps({'data': {'serviceInstanceUpdate': self.acknowledge}})
            if "project(id" in args[1]:
                return json.dumps({"data": {"project": {
                    "environments": {"edges": [{"node": {"id": "environment"}}]},
                    "services": {"edges": [{"node": {"id": "index-service", "name": "weaviate"}}]}}}})
            return json.dumps({"data": {"serviceInstance": {
                "serviceId": "index-service", "environmentId": "environment",
                "source": {"image": self.image},
                "dockerfilePath": self.dockerfile,
                "latestDeployment": {"id": "deployment", "status": self.deployment_status},
                "activeDeployments": [{"id": "deployment", "status": "SUCCESS",
                    "deploymentStopped": False, "instances": self.instances}]},
                "serviceInstanceLimitOverride": self.budgets}})
        if args[:2] == ["variable", "list"]:
            return json.dumps(self.variables)
        if args[0] == "ssh":
            return self.startup
        return ""

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('infrastructure', ROOT / 'deploy/infrastructure.py')
infra = importlib.util.module_from_spec(spec)
spec.loader.exec_module(infra)


class Infrastructure(unittest.TestCase):
    def test_deployment_clis_load_from_the_documented_script_paths(self):
        for script in ('deploy/railway/deploy.py', 'deploy/railway/provision.py',
                       'deploy/railway/infrastructure.py', 'deploy/compose/infrastructure.py'):
            with self.subTest(script=script):
                result = subprocess.run([sys.executable, str(ROOT / script), '--help'],
                                        capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('usage:', result.stdout)

    def test_profiles_and_literal_overrides_reach_the_launch_model(self):
        small = infra.resolve()
        self.assertEqual(small['weaviate']['environment']['ASYNC_INDEXING'], 'false')
        self.assertEqual(small['postgres']['environment']['QUIVR_POSTGRES_RANDOM_PAGE_COST'], '1.1')
        large = infra.resolve('large')
        self.assertEqual(large['postgres']['environment']['QUIVR_POSTGRES_MAX_CONNECTIONS'], '500')
        self.assertEqual(large['postgres']['environment']['QUIVR_POSTGRES_SHARED_BUFFERS'], '10GB')
        self.assertEqual(large['postgres']['environment']['QUIVR_POSTGRES_JIT'], 'on')
        self.assertEqual(large['postgres']['environment']['QUIVR_POSTGRES_SYNCHRONOUS_COMMIT'], 'on')
        self.assertEqual(large['postgres']['environment']['QUIVR_POSTGRES_VOLUME_MB'], '476837')
        self.assertEqual(large['postgres']['x-quivr-storage-budget-bytes'], '500000000000')
        self.assertEqual(large['weaviate']['environment']['GOMEMLIMIT'], '27GiB')
        self.assertEqual(large['weaviate']['deploy']['resources']['limits'], {'memory': '32000000000', 'cpus': '32'})
        self.assertEqual(large['autoscaler']['environment']['QUIVR_AUTOSCALER_MIN'], '1')
        self.assertEqual(large['weaviate']['environment']['ASYNC_INDEXING'], 'true')
        self.assertEqual(large['weaviate']['environment']['PERSISTENCE_MEMTABLES_MAX_SIZE_MB'], '1024')
        rendered = subprocess.run([sys.executable, str(ROOT / 'deploy/infrastructure.py'), 'render'],
                                  env={**os.environ, 'QUIVR_POSTGRES_MAX_CONNECTIONS': '321'},
                                  capture_output=True, text=True, timeout=5, check=True)
        self.assertEqual(json.loads(rendered.stdout)['services']['postgres']['environment']
                         ['QUIVR_POSTGRES_MAX_CONNECTIONS'], '321')
        with tempfile.TemporaryDirectory() as directory:
            override = Path(directory) / 'installation.json'
            override.write_text('''{"services":{"weaviate":{"environment":{"GOMEMLIMIT":"3500MiB"},
                "deploy":{"resources":{"limits":{"memory":"4294967296"}}}}}}''')
            resolved = infra.resolve(overrides=override)
            model = infra.compose_model(resolved)
            self.assertEqual(model['services']['weaviate']['environment']['GOMEMLIMIT'], '3500MiB')
            self.assertEqual(model['services']['weaviate']['deploy']['resources']['limits']['memory'], '4294967296')
            self.assertEqual(model['services']['postgres']['environment']['QUIVR_POSTGRES_RANDOM_PAGE_COST'], '1.1')
            override.write_text('{"services":{"weaviate":{"environment":{"API_KEY":"private"}}}}')
            with self.assertRaisesRegex(ValueError, 'unsupported infrastructure setting'):
                infra.resolve(overrides=override)
            for role in ('postgres', 'autoscaler'):
                override.write_text(json.dumps({'services': {role: {'image': 'registry/image@sha256:' + 'a' * 64}}}))
                with self.assertRaisesRegex(ValueError, 'image overrides'):
                    infra.resolve(overrides=override)
            for key, value in (
                ('QUIVR_POSTGRES_MEMORY_MB', '64'), ('QUIVR_POSTGRES_VOLUME_MB', '0'),
                ('QUIVR_POSTGRES_MAX_CONNECTIONS', '262144'), ('QUIVR_POSTGRES_WORK_MEM', '1GiB'),
                ('QUIVR_POSTGRES_WORK_MEM', '0MB'), ('QUIVR_POSTGRES_MAX_WAL_SIZE', '16MB'),
                ('QUIVR_POSTGRES_MIN_WAL_SIZE', '16MB'), ('QUIVR_POSTGRES_MIN_WAL_SIZE', '64GB'), ('QUIVR_POSTGRES_EFFECTIVE_IO_CONCURRENCY', '1001'),
                ('QUIVR_POSTGRES_RANDOM_PAGE_COST', '10000000001'),
                ('QUIVR_POSTGRES_CHECKPOINT_TIMEOUT', '29s'), ('QUIVR_POSTGRES_CHECKPOINT_TIMEOUT', '2h')):
                with self.subTest(key=key, value=value):
                    override.write_text(json.dumps({'services': {'postgres': {'environment': {key: value}}}}))
                    with self.assertRaisesRegex(ValueError, 'invalid infrastructure value'):
                        infra.resolve(overrides=override)
            for invalid_image in (None, 123, [], 'registry/image:mutable'):
                override.write_text(json.dumps({'services': {'weaviate': {'image': invalid_image}}}))
                with self.assertRaisesRegex(ValueError, 'immutable digest'):
                    infra.resolve(overrides=override)

    def test_railway_apply_is_targeted_idempotent_and_never_sets_replicas(self):
        from deploy.railway.infrastructure import Railway
        transport = RailwayTransport()
        adapter = Railway('project', 'environment', run=transport)
        adapter.select({'weaviate': 'index-service'})
        desired = {'weaviate': {'image': 'registry/image@sha256:' + 'a' * 64,
                               'environment': {'ASYNC_INDEXING': 'true'},
                               'deploy': {'resources': {'limits': {'memory': '2000000000', 'cpus': '2'}}}}}
        adapter.apply(desired)
        writes = [(args, stdin) for args, stdin in transport.calls if args[:2] == ['variable', 'set']]
        self.assertEqual(writes, [
            (['variable', 'set', 'ASYNC_INDEXING', '--stdin', '--skip-deploys',
              '--project', 'project', '--environment', 'environment', '--service', 'index-service'], 'true')])
        self.assertFalse(any('mutation' in args[1] for args, _ in transport.calls if args[0] == 'api'))
        transport.calls.clear()
        transport.variables['ASYNC_INDEXING'] = 'true'
        adapter.apply(desired)
        self.assertFalse(any(args[:2] == ['variable', 'set'] or (args[0] == 'api' and 'mutation' in args[1])
                             for args, _ in transport.calls))

    def test_drift_distinguishes_configured_and_runtime_and_masks_invalid_values(self):
        from deploy.railway.infrastructure import Railway
        transport = RailwayTransport()
        adapter = Railway('project', 'environment', run=transport)
        adapter.select({'weaviate': 'index-service'})
        desired = {'weaviate': {'environment': {'ASYNC_INDEXING': 'true'}}}
        transport.variables['ASYNC_INDEXING'] = 'true'
        result = adapter.check(desired)
        self.assertEqual(result['status'], 'drift')
        observed = {(row['scope'], row['setting']): row for row in result['observations']}
        self.assertEqual(observed['configured', 'ASYNC_INDEXING']['status'], 'match')
        self.assertEqual(observed['startup_environment', 'ASYNC_INDEXING']['status'], 'drift')
        transport.startup = ''
        self.assertEqual(adapter.check(desired)['status'], 'unknown')
        transport.variables['ASYNC_INDEXING'] = 'private-token-value'
        self.assertNotIn('private-token-value', json.dumps(adapter.check(desired)))
        transport.variables['ASYNC_INDEXING'] = 'true'
        transport.startup = 'ASYNC_INDEXING=true\n'
        transport.deployment_status = 'DEPLOYING'
        transport.calls.clear()
        self.assertEqual(adapter.check(desired)['status'], 'unknown')
        self.assertFalse(any(args[0] == 'ssh' for args, _ in transport.calls))
        self.assertFalse(any(args[:2] == ['variable', 'set'] or (args[0] == 'api' and 'mutation' in args[1])
                             for args, _ in transport.calls))
        transport.deployment_status = 'SUCCESS'
        transport.variables['ASYNC_INDEXING'] = 'true'
        transport.startup = 'ASYNC_INDEXING=true\n'
        transport.instances.append({'id': 'instance-two', 'status': 'RUNNING'})
        transport.calls.clear()
        self.assertEqual(adapter.check(desired)['status'], 'match')
        self.assertEqual([args[args.index('--deployment-instance') + 1]
                          for args, _ in transport.calls if args[0] == 'ssh'], ['instance', 'instance-two'])

    def test_wrong_environment_is_refused_before_any_write(self):
        from deploy.railway.infrastructure import Railway
        response = {'data': {'project': {'environments': {'edges': [{'node': {'id': 'other'}}]},
                                        'services': {'edges': [], 'pageInfo': {'hasNextPage': False}}}}}
        transport = Mock(return_value=json.dumps(response))
        adapter = Railway('project', 'environment', run=transport)
        with self.assertRaisesRegex(RuntimeError, 'environment is not in the selected project'):
            adapter.select({'weaviate': 'index-service'})
        self.assertEqual(transport.call_count, 1)

    def test_stock_postgres_switches_to_the_managed_build_without_replica_writes(self):
        from deploy.railway.infrastructure import Railway
        transport = RailwayTransport()
        adapter = Railway('project', 'environment', run=transport)
        adapter.select({'postgres': 'index-service'})
        adapter.apply({'postgres': {'environment': {}}})
        updates = [json.loads(args[3]) for args, _ in transport.calls
                   if args[0] == 'api' and args[1].startswith('mutation')]
        self.assertEqual(updates, [{'serviceId': 'index-service', 'environmentId': 'environment',
                                   'input': {'dockerfilePath': 'deploy/railway/postgres.Dockerfile',
                                             'source': {'image': None}}}])
        transport.calls.clear()
        adapter.apply({'postgres': {'environment': {}}})
        self.assertFalse(any(args[0] == 'api' and args[1].startswith('mutation')
                             for args, _ in transport.calls))

    def test_image_deployment_is_scoped_to_the_selected_environment(self):
        provision_spec = importlib.util.spec_from_file_location('provision', ROOT / 'deploy/railway/provision.py')
        provision = importlib.util.module_from_spec(provision_spec)
        provision_spec.loader.exec_module(provision)
        with patch.dict(sys.modules, {'provision': provision}):
            module_spec = importlib.util.spec_from_file_location('railway_deploy', ROOT / 'deploy/railway/deploy.py')
            deployment = importlib.util.module_from_spec(module_spec)
            module_spec.loader.exec_module(deployment)
        with tempfile.TemporaryDirectory() as directory:
            deployment.ROOT = Path(directory)
            state = deployment.ROOT / '.scratch/railway/project'
            state.mkdir(parents=True)
            (state / 'services.json').write_text('{"weaviate":"index-service"}')
            deployment.cli = Mock(side_effect=lambda *args, **kwargs: json.dumps({
                'id': 'project', 'name': 'quivr-v2-demo',
                'environments': {'edges': [{'node': {'id': 'environment'}}]}}) if args[0] == 'status' else '{}')
            with patch.object(sys, 'argv', ['deploy.py', '--project-id', 'project',
                                           '--environment-id', 'environment', 'weaviate']):
                deployment.main()
            source_writes = [call.args for call in deployment.cli.call_args_list if call.args[0] == 'api']
            self.assertEqual(len(source_writes), 1)
            self.assertEqual(json.loads(source_writes[0][3]), {
                'serviceId': 'index-service', 'environmentId': 'environment',
                'input': {'source': {'image': infra.resolve()['weaviate']['image']}}})
            self.assertIn(('redeploy', '--service', 'index-service', '--project', 'project',
                           '--environment', 'environment', '--from-source', '--yes', '--json'),
                          [call.args for call in deployment.cli.call_args_list])
            self.assertFalse(any(call.args[:2] == ('service', 'source')
                                 for call in deployment.cli.call_args_list))

    def test_provider_acknowledgement_and_resource_units_are_required(self):
        from deploy.railway.infrastructure import Railway
        transport = RailwayTransport()
        adapter = Railway('project', 'environment', run=transport)
        adapter.select({'weaviate': 'index-service'})
        desired = {'weaviate': {'image': 'registry/image@sha256:' + 'b' * 64,
                               'deploy': {'resources': {'limits': {'memory': '4294967296', 'cpus': '4'}}}}}
        adapter.apply(desired)
        mutations = [json.loads(args[3]) for args, _ in transport.calls
                     if args[0] == 'api' and args[1].startswith('mutation')]
        self.assertEqual(mutations, [
            {'input': {'serviceId': 'index-service', 'environmentId': 'environment',
                       'memoryGB': 4.294967296, 'vCPUs': 4.0}},
            {'serviceId': 'index-service', 'environmentId': 'environment',
             'input': {'source': {'image': 'registry/image@sha256:' + 'b' * 64}}}])
        transport.calls.clear()
        adapter.apply(desired)
        self.assertFalse(any(args[0] == 'api' and args[1].startswith('mutation')
                             for args, _ in transport.calls))
        transport.image = 'registry/image@sha256:' + 'a' * 64
        transport.acknowledge = False
        with self.assertRaisesRegex(RuntimeError, 'did not acknowledge'):
            adapter.apply(desired)

    def test_compose_reports_sql_and_inspects_only_the_selected_project(self):
        from deploy.compose.infrastructure import Compose
        settings = infra.resolve('large')
        calls = []
        storage_bytes = 343597383680
        def docker(args):
            calls.append(args)
            if args[0] == 'ps':
                self.assertIn('label=com.docker.compose.project=fixture', args)
                return 'database' if args[-1].endswith('postgres') else ''
            if args[0] == 'inspect':
                return json.dumps([{'Config': {
                    'Image': settings['postgres']['image'], 'Env': ['POSTGRES_PASSWORD=private'],
                    'Labels': {'com.docker.compose.project': 'fixture', 'com.docker.compose.service': 'postgres'}},
                    'State': {'Running': True}, 'HostConfig': {'Memory': 1073741824, 'NanoCpus': 1000000000}}])
            if args[0] == 'exec':
                return ('{"settings":{"random_page_cost":"1.1","effective_io_concurrency":"200"},"pg_stat_statements":true,"statistics_preloaded":true}\n327680\n__memory=1073741824\n__cpus=1\n'
                        + '__storage=' + str(storage_bytes) + '\n')
            self.fail('unexpected mutating command')
        adapter = Compose('fixture', run=docker)
        result = adapter.check(settings)
        observed = {(row['service'], row['scope'], row['setting']): row for row in result['observations']}
        self.assertEqual(observed['postgres', 'effective_sql', 'random_page_cost']['status'], 'match')
        self.assertEqual(observed['postgres', 'effective_sql', 'pg_stat_statements']['status'], 'match')
        self.assertEqual(observed['postgres', 'runtime_resources', 'memory']['observed'], '1073741824')
        self.assertEqual(observed['weaviate', 'configured', 'image']['status'], 'unknown')
        self.assertEqual(observed['postgres', 'filesystem_capacity', 'minimum_bytes']['status'], 'drift')
        self.assertNotIn('private', json.dumps(result))
        self.assertEqual({args[0] for args in calls}, {'ps', 'inspect', 'exec'})
        storage_bytes = 600000000000
        result = adapter.check(settings)
        capacity = next(row for row in result['observations']
                        if row['service'] == 'postgres' and row['scope'] == 'filesystem_capacity')
        self.assertEqual(capacity['status'], 'match')

    def test_image_reporting_consumers_follow_the_shared_pins(self):
        with patch.object(sys, 'path', [str(ROOT / 'scripts'), *sys.path]):
            import weaviate_upgrade, inventory, measure, load, load_stack
        def docker(command, **kwargs):
            if 'up' in command:
                raise RuntimeError('isolated startup stopped')
            return subprocess.CompletedProcess(command, 0)
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'upgrade'
            with patch.object(weaviate_upgrade.subprocess, 'run', side_effect=docker):
                with self.assertRaisesRegex(RuntimeError, 'isolated startup stopped'):
                    weaviate_upgrade.exercise(target)
            report = json.loads((target / 'report.json').read_text())
            self.assertEqual(report['images'][-1], infra.resolve(environ={})['weaviate']['image'])
            self.assertEqual(report['result'], 'failed')
            declared = infra.resolve(environ={})
            self.assertTrue({declared['postgres']['image'], declared['weaviate']['image']}.issubset(
                {row['image'] for row in inventory.images()}))
            with patch.object(measure, 'output', return_value='local'), patch.dict(os.environ, {}, clear=True):
                self.assertTrue({declared['postgres']['image'], declared['weaviate']['image']}.issubset(
                    set(measure.pins()['images'])))
            output = Path(directory) / 'load'
            with patch.object(load, 'read', return_value={'name': 'local', 'search': {'concurrency': 1}}), \
                    patch.object(load_stack, 'local_docker_host', return_value='unix:///local.sock'), \
                    patch.object(load, 'machine', return_value={'system': 'local', 'architecture': 'amd64',
                                                               'cpus': 1, 'memory_bytes': 1024}), \
                    patch.object(load, 'output', return_value='local'), \
                    patch.object(load, 'run_scenario', side_effect=RuntimeError('measurement not executed')), \
                    patch.dict(os.environ, {}, clear=True):
                with self.assertRaisesRegex(RuntimeError, 'measurement not executed'):
                    load.main(['--scenario', 'local.yaml', '--out', str(output)])
            services = json.loads((output / 'report.json').read_text())['versions']['services']
            self.assertEqual(services['postgres'], declared['postgres']['image'])
            self.assertEqual(services['weaviate'], declared['weaviate']['image'])

    def test_v1_cpu_probe_reads_nested_combined_mount_and_parent_limits(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            mount = root / 'cgroup/cpu,cpuacct'
            child = mount / 'parent/child'
            child.mkdir(parents=True)
            for path, quota in ((child, '400000'), (child.parent, '200000')):
                (path / 'cpu.cfs_quota_us').write_text(quota)
                (path / 'cpu.cfs_period_us').write_text('100000')
            membership = root / 'membership'
            membership.write_text('3:cpu,cpuacct:/parent/child\n')
            script = infra.runtime_script('api').replace('/sys/fs/cgroup', str(root / 'cgroup')).replace(
                '/proc/self/cgroup', str(membership))
            result = subprocess.run(['sh', '-c', script], capture_output=True, text=True, timeout=5, check=True)
            self.assertEqual(infra.observed_resources(result.stdout.splitlines())['cpus'], '2')

    def test_compose_apply_loads_selected_local_state_without_exported_variables(self):
        from deploy.compose import infrastructure as compose_module
        Compose = compose_module.Compose
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True):
            root = Path(directory)
            project = root / '.scratch/selected'
            project.mkdir(parents=True)
            (project / 'state.json').write_text('{"password":"fixture-password"}')
            run = Mock(return_value='')
            with patch.object(compose_module.infra, 'ROOT', root):
                Compose('selected', run=run).apply(project / 'infrastructure.json')
            environment = run.call_args.kwargs['environ']
            self.assertEqual(environment['QUIVR_DB_PASSWORD'], 'fixture-password')
            self.assertEqual(environment['QUIVR_LOCAL_ROOT'], str(project))
            self.assertEqual(environment['QUIVR_MODEL_ROOT'], str(root / '.scratch/e5-model'))
            self.assertEqual(run.call_args.args[0][-4:], ['up', '-d', 'postgres', 'weaviate'])
            with patch.object(compose_module.infra, 'ROOT', root):
                with self.assertRaisesRegex(RuntimeError, 'local project state'):
                    Compose('missing', run=run).apply(root / 'overlay.json')

    def test_runtime_capacity_probe_keeps_the_database_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            location = Path(directory)
            (location / 'psql').write_text("#!/bin/sh\nprintf '{}\\n'\n")
            (location / 'df').write_text(
                '#!/bin/sh\nsize=8192\n[ "$2" != "$PGDATA" ] || size=4096\n'
                'printf "Filesystem 1024-blocks Used Available Capacity Mounted\\n"\n'
                'printf "fixture %s 0 %s 0%% /\\n" "$size" "$size"\n')
            for command in ('psql', 'df'):
                (location / command).chmod(0o755)
            result = subprocess.run(['sh', '-c', infra.runtime_script('postgres')],
                                    env={'PATH': str(location) + ':/usr/bin:/bin', 'PGDATA': directory},
                                    capture_output=True, text=True, timeout=5, check=True)
            self.assertEqual(infra.observed_resources(result.stdout.splitlines())['storage'], '4194304')

    def test_reprovisioning_preserves_the_autoscalers_replica_count(self):
        from deploy.railway.provision import deployment_config, load_services
        services = load_services('large')
        self.assertEqual(services['weaviate']['variables']['ASYNC_INDEXING'], 'true')
        self.assertEqual(services['postgres']['variables']['QUIVR_POSTGRES_EFFECTIVE_CACHE_SIZE'], '25GB')
        for name in ('api', 'worker', 'worker-bulk'):
            self.assertEqual(services[name]['limits'], {'memory': '32000000000', 'cpus': '32'})
        worker = services['worker-bulk']
        self.assertEqual(deployment_config(worker, new=True)['numReplicas'], 1)
        existing = deployment_config(worker, new=False)
        self.assertNotIn('numReplicas', existing)
        self.assertNotIn('multiRegionConfig', existing)
        self.assertEqual(existing['dockerfilePath'], 'deploy/railway/core.Dockerfile')


if __name__ == '__main__':
    unittest.main()
