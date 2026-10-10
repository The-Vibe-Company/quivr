#!/usr/bin/env python3
"""Opt-in isolated pinned-Weaviate warm-up smoke measurement, never production tuning.

Supply reserved loopback HTTP, gRPC, gossip, data and Raft ports and a reserved
collection. This small fixture proves the deployed probes work after import and
restart; it does not reproduce multi-million-object cold-storage latency.
"""
import argparse
import json
import os
from pathlib import Path
import random
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from deploy import infrastructure


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ports', required=True, help='six reserved ports: HTTP,gRPC,gossip,data,Raft,internal-Raft')
    parser.add_argument('--collection', required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--warmup-binary', type=Path, required=True)
    parser.add_argument('--objects', type=int, default=2000)
    parser.add_argument('--without-cgroup-limits', action='store_true',
                        help='VM fallback; report the lack of container CPU/memory limits')
    args = parser.parse_args()
    ports = [int(value) for value in args.ports.split(',')]
    if len(ports) != 6 or len(set(ports)) != 6 or any(not 1024 <= port <= 65535 for port in ports):
        parser.error('provide six distinct reserved ports')
    if not args.collection.isascii() or not args.collection.isalnum() or not args.collection[0].isupper():
        parser.error('collection must be an alphanumeric GraphQL class name')
    if not 1 <= args.objects <= 100000:
        parser.error('this bounded smoke fixture accepts 1..100000 objects')
    args.out.mkdir(parents=True, exist_ok=True)
    url = f'http://127.0.0.1:{ports[0]}'
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + 600
    image = infrastructure.resolve(environ={})['weaviate']['image']
    name = 'quivr-cold-' + uuid.uuid4().hex[:12]
    report = {'kind': 'functional-smoke', 'image': image, 'objects': args.objects,
              'dimensions': 768, 'quantization': 'rq-8', 'rows': [],
              'container_limits': None if args.without_cgroup_limits else {'memory': '2GiB', 'cpus': 2},
              'limitations': ['Small local fixture; no reference-scale latency claim.',
                              'Direct store timings exclude engine overhead.',
                              'Probes share state and can warm it; not independent cold trials.']}

    def docker(*argv):
        result = subprocess.run(['docker', *argv], capture_output=True, text=True,
                                timeout=max(1, min(60, deadline-time.monotonic())))
        if result.returncode:
            raise RuntimeError('owned local Docker command failed')
        return result.stdout

    def request(path, body=None):
        req = urllib.request.Request(url+path, data=json.dumps(body).encode() if body is not None else None,
                                     headers={'Content-Type': 'application/json'})
        with opener.open(req, timeout=max(1, min(30, deadline-time.monotonic()))) as response:
            result = json.load(response)
        if isinstance(result, dict) and result.get('errors'):
            raise RuntimeError('pinned store query failed')
        return result

    def ready():
        while time.monotonic() < deadline:
            if docker('inspect', '--format', '{{.State.Running}}', name).strip() != 'true':
                raise RuntimeError('owned pinned store exited before readiness')
            try:
                with opener.open(url+'/v1/.well-known/ready', timeout=1) as response:
                    if response.status == 200:
                        return
            except OSError:
                pass
            time.sleep(.1)  # Measurement lane: condition polling, not a test wait.
        raise RuntimeError('pinned store did not become ready before the run deadline')

    def warmup(phase):
        start = time.monotonic()
        result = subprocess.run([str(args.warmup_binary.resolve()), '--once', '--url', url],
                                capture_output=True, text=True,
                                timeout=max(1, min(130, deadline-time.monotonic())))
        row = {'phase': phase, 'seconds': time.monotonic()-start, 'returncode': result.returncode}
        row['warmup'] = json.loads(result.stdout)
        report['rows'].append(row)
        if result.returncode or row['warmup']['errors'] or (phase != 'empty-start' and
                (row['warmup']['near_vector'] != 1 or row['warmup']['bm25'] != 1)):
            raise RuntimeError('deployment probes did not warm the populated index')

    def probes(phase, vector):
        branches = {'lexical': 'bm25:{query:"Harbour",properties:["text"]}',
                    'semantic': 'nearVector:{vector:'+json.dumps(vector)+',targetVectors:["primary"]}',
                    'hybrid': 'hybrid:{query:"Harbour",vector:'+json.dumps(vector)+',targetVectors:["primary"]}'}
        for mode, branch in branches.items():
            start = time.monotonic()
            result = request('/v1/graphql', {'query': '{Get{'+args.collection+'(limit:1,'+branch+'){_additional{id}}}}'})
            hits = result['data']['Get'][args.collection]
            report['rows'].append({'phase': phase, 'mode': mode, 'seconds': time.monotonic()-start,
                                   'hits': len(hits)})
            if not hits:
                raise RuntimeError('search returned no expected fixture content')

    try:
        # A host network is deliberate: all six ports are explicitly reserved and
        # each listener binds to this isolated VM; no production endpoint is used.
        limits = [] if args.without_cgroup_limits else ['--memory', '2g', '--cpus', '2']
        docker('run', '-d', '--name', name, '--network', 'host', *limits,
               '-e', 'AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED=true', '-e', 'AUTOSCHEMA_ENABLED=false',
               '-e', 'DEFAULT_VECTORIZER_MODULE=none', '-e', 'DEFAULT_QUANTIZATION=rq-8',
               '-e', 'PERSISTENCE_DATA_PATH=/var/lib/weaviate', '-e', 'CLUSTER_HOSTNAME='+name,
               '-e', 'CLUSTER_ADVERTISE_ADDR=127.0.0.1', '-e', 'CLUSTER_ADVERTISE_PORT='+str(ports[2]),
               '-e', 'CLUSTER_GOSSIP_BIND_PORT='+str(ports[2]), '-e', 'CLUSTER_DATA_BIND_PORT='+str(ports[3]),
               '-e', 'GRPC_PORT='+str(ports[1]), '-e', 'RAFT_PORT='+str(ports[4]),
               '-e', 'RAFT_INTERNAL_RPC_PORT='+str(ports[5]), '-e', 'DISABLE_TELEMETRY=true',
               '-e', 'GOMEMLIMIT=1700MiB', image, '--host', '127.0.0.1', '--port', str(ports[0]), '--scheme', 'http')
        ready()
        warmup('empty-start')
        request('/v1/schema', {'class': args.collection, 'properties': [{'name': 'text', 'dataType': ['text']}],
                              'vectorConfig': {'primary': {'vectorizer': {'none': None}, 'vectorIndexType': 'hnsw',
                              'vectorIndexConfig': {'rq': {'enabled': True, 'bits': 8}}}}})
        rng = random.Random(42)
        first = None
        for start in range(0, args.objects, 100):
            objects = []
            for index in range(start, min(args.objects, start+100)):
                vector = [rng.uniform(-1, 1) for _ in range(768)]
                first = first or vector
                objects.append({'class': args.collection, 'id': str(uuid.uuid5(uuid.NAMESPACE_OID, str(index))),
                                'properties': {'text': 'Harbour ferry opens '+str(index)}, 'vectors': {'primary': vector}})
            results = request('/v1/batch/objects', {'objects': objects})
            if any(row.get('result', {}).get('errors') for row in results):
                raise RuntimeError('fixture import rejected')
        probes('after-import-before-warmup', first)
        warmup('after-import')
        probes('after-import-after-warmup', first)
        docker('restart', name)
        ready()
        warmup('after-restart')
        probes('after-restart-after-warmup', first)
        report['result'] = 'passed'
    except Exception as error:
        report['result'] = 'failed'
        # Persist only a controlled diagnostic, never provider/body/environment output.
        report['error_type'] = type(error).__name__
        raise
    finally:
        (args.out/'report.json').write_text(json.dumps(report, indent=2)+'\n')
        # A failed Docker start can still leave a Created container behind.
        subprocess.run(['docker', 'rm', '-f', '-v', name], capture_output=True, timeout=30)


if __name__ == '__main__':
    def stopped(_signum, _frame):
        raise SystemExit(1)
    signal.signal(signal.SIGTERM, stopped)
    main()
