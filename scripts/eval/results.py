"""Shared search measurements with an owner-only JSON outbox and MLflow tracking.

No tracking dependency; paired comparison needs scipy (evaluation requirements).
"""
import argparse
import base64
import hashlib
import json
import math
import os
import pathlib
import re
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

# Set before any optional tracking import, including callers importing this helper.
os.environ['MLFLOW_DISABLE_TELEMETRY'] = 'true'
os.environ['DO_NOT_TRACK'] = 'true'
os.environ['MLFLOW_DISABLE_AGENT_HINT'] = '1'
ROOT = pathlib.Path(__file__).resolve().parents[2]


def normalized(key):
    name = key.replace('@', '_at_')
    if not re.fullmatch(r'[\w .:/-]+', name):
        raise ValueError('unsupported metric name')
    return name


def encode(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False)


def normalize(values):
    out = {}
    for key, value in values.items():
        name = normalized(key)
        if name in out:
            raise ValueError('metric names collide after normalization')
        out[name] = value
    return out


def record(value):
    value = json.loads(encode(value))  # detach from the caller and reject NaN/Infinity
    allowed = {'schema_version', 'experiment', 'git_sha', 'plugin_digest', 'config', 'dataset', 'tier',
               'machine', 'duration_seconds', 'cost', 'metrics', 'per_query', 'provenance'}
    if set(value) - allowed:
        raise ValueError('unknown measurement fields')
    if value['schema_version'] != 1 or value['tier'] not in ('direct', 'engine'):
        raise ValueError('unsupported schema version or tier')
    if (not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9/_.-]*', value['experiment'])
            or value['experiment'].startswith('private/')):
        raise ValueError('experiment must name the aggregate namespace')
    for field in ('git_sha', 'plugin_digest', 'config', 'machine', 'duration_seconds', 'cost'):
        if field not in value:
            raise ValueError('missing lineage: ' + field)
    if not isinstance(value['config'], dict) or not isinstance(value['cost'], dict):
        raise ValueError('config and cost must be objects')
    if value['duration_seconds'] is not None and (type(value['duration_seconds']) not in (int, float) or value['duration_seconds'] < 0):
        raise ValueError('duration must be a non-negative number or null')
    for field in ('name', 'version', 'split', 'fingerprint', 'private'):
        if field not in value['dataset']:
            raise ValueError('missing dataset lineage: ' + field)
    if type(value['dataset']['private']) is not bool:
        raise ValueError('dataset.private must be a boolean')
    value['metrics'] = normalize(value['metrics'])
    for metric in value['metrics'].values():
        if metric is not None and (type(metric) not in (int, float) or not math.isfinite(metric)):
            raise ValueError('metrics must be finite numbers or null')
    value['per_query'] = normalize(value.get('per_query', {}))
    value['per_query_status'] = ('private' if value['dataset']['private'] else 'available') if value['per_query'] else 'missing'
    for scores in value['per_query'].values():
        if not isinstance(scores, dict) or any(type(v) not in (int, float) for v in scores.values()):
            raise ValueError('per_query must map metric names to query score maps')
    identity = {k: value[k] for k in ('experiment', 'git_sha', 'plugin_digest', 'config', 'dataset', 'tier')}
    value['result_key'] = hashlib.sha256(encode(identity).encode()).hexdigest()
    return value


def summary(value):
    return {k: v for k, v in value.items() if k != 'per_query'}


def result_key(value):
    key = value.get('result_key')
    if not isinstance(key, str) or not re.fullmatch(r'[0-9a-f]{64}', key):
        raise ValueError('invalid result key')
    return key


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(path.parent, 0o700)
    fd, name = tempfile.mkstemp(dir=path.parent, prefix='.result-')
    try:
        with os.fdopen(fd, 'w') as output:
            output.write(encode(value) + '\n')
            output.flush()
            os.fsync(output.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


class Unavailable(Exception):
    """Retryable tracking failure; never contains server text or credentials."""


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Tracking:
    """Small tracking REST client. Credentials belong to each client, not globals."""
    def __init__(self, uri, username=None, password=None):
        parsed = urllib.parse.urlsplit(uri)
        if (parsed.scheme not in ('https', 'http') or not parsed.netloc or parsed.username
                or parsed.password or parsed.query or parsed.fragment
                or parsed.scheme == 'http' and parsed.hostname not in ('localhost', '127.0.0.1', '::1')):
            raise ValueError('tracking URI must use HTTPS (HTTP allowed for loopback), without credentials')
        if bool(username) != bool(password):
            raise ValueError('both tracking username and password are required')
        self.uri = uri.rstrip('/')
        self.headers = {'Content-Type': 'application/json'}
        if username:
            auth = base64.b64encode((username + ':' + password).encode()).decode()
            self.headers['Authorization'] = 'Basic ' + auth
        self.opener = urllib.request.build_opener(NoRedirect())

    def request(self, path, body=None, method=None):
        request = urllib.request.Request(self.uri + path, headers=self.headers,
                                         data=encode(body).encode() if body is not None else None,
                                         method=method or ('POST' if body is not None else 'GET'))
        try:
            with self.opener.open(request, timeout=10) as response:
                raw = response.read()
            return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as error:
            status = error.code
            error.close()
            if status in (401, 403):
                raise PermissionError('MLflow authorization failed (HTTP ' + str(status) + ')') from None
            if status in (408, 429) or status >= 500:
                raise Unavailable('tracking store unavailable') from None
            raise ValueError('MLflow rejected request (HTTP ' + str(status) + ')') from None
        except (urllib.error.URLError, TimeoutError, ConnectionError):
            raise Unavailable('tracking store unavailable') from None

    def experiment(self, name, create=False):
        # Search avoids get-by-name's missing-experiment HTTP 404.
        found = self.request('/api/2.0/mlflow/experiments/search', {'max_results': 1000,
                             'filter': "name = '" + name.replace("'", "\\'") + "'"})
        experiments = found.get('experiments', [])
        if experiments:
            return experiments[0]['experiment_id']
        if not create:
            return None
        return self.request('/api/2.0/mlflow/experiments/create', {'name': name})['experiment_id']

    def runs(self, experiment_id, key=None):
        body = {'experiment_ids': [experiment_id], 'max_results': 1000,
                'filter': "tags.`quivr.result_key` = '" + key + "'" if key else "tags.`quivr.result_key` LIKE '%'"}
        output = []
        while True:
            page = self.request('/api/2.0/mlflow/runs/search', body)
            output.extend(page.get('runs', []))
            if not page.get('next_page_token'):
                return output
            body['page_token'] = page['next_page_token']

    def artifact(self, run, name, value=None):
        uri = urllib.parse.urlsplit(run['info']['artifact_uri'])
        if uri.scheme != 'mlflow-artifacts' or uri.netloc:
            raise ValueError('tracking server must proxy artifacts (mlflow-artifacts:/)')
        path = '/api/2.0/mlflow-artifacts/artifacts/' + uri.path.strip('/') + '/' + name
        return self.request(path, value, 'PUT' if value is not None else 'GET')

    def upload(self, value, experiment, create=True):
        eid = self.experiment(experiment, create=create)
        if eid is None:
            raise PermissionError('private experiment must be provisioned by its owner')
        matches = self.runs(eid, value['result_key'])
        run = next((r for r in matches if r['info']['status'] == 'FINISHED'), None)
        if run:
            return run['info']['run_id']
        run = matches[0] if matches else self.request('/api/2.0/mlflow/runs/create', {
            'experiment_id': eid, 'start_time': int(time.time() * 1000),
            'tags': [{'key': 'quivr.result_key', 'value': value['result_key']},
                     {'key': 'mlflow.runName', 'value': value['config'].get('model', value['result_key'][:12])}]})['run']
        rid = run['info']['run_id']
        fields = summary(value)
        # MLflow truncates oversized params. Artifacts own the complete record;
        # scalar params are a bounded convenience for the compare UI and search.
        searchable = {k: v for k, v in fields.items() if k != 'metrics' and len(encode(v)) <= 6000}
        searchable['quivr.record_artifact_only'] = any(len(encode(v)) > 6000 for k, v in fields.items() if k != 'metrics')
        searchable.update({'dataset.' + k: v for k, v in value['dataset'].items()})
        searchable.update({'config.' + k: v for k, v in value['config'].items()
                           if not isinstance(v, (dict, list))})
        params = [{'key': k, 'value': encode(v)} for k, v in searchable.items()
                  if len(k) <= 250 and len(encode(v)) <= 6000]
        metrics = dict(value['metrics'])
        # Numeric settings stay queryable despite MLflow params being strings.
        def numbers(prefix, values):
            for key, v in values.items():
                name = normalized(prefix + key)
                if isinstance(v, dict):
                    numbers(name + '.', v)
                elif type(v) in (int, float):
                    metrics[name] = v
        numbers('config.', value['config'])
        numbers('cost.', value['cost'])
        if value['duration_seconds'] is not None:
            metrics['duration_seconds'] = value['duration_seconds']
        self.request('/api/2.0/mlflow/runs/log-batch', {'run_id': rid, 'params': params,
            'metrics': [{'key': key, 'value': v, 'timestamp': int(time.time() * 1000), 'step': 0}
                        for key, v in metrics.items() if v is not None], 'tags': []})
        self.artifact(run, 'record.json', fields)
        if value.get('comparison'):
            self.artifact(run, 'comparison.json', value['comparison'])
        if value.get('per_query'):
            self.artifact(run, 'per_query.json', value['per_query'])
        self.request('/api/2.0/mlflow/runs/update', {'run_id': rid, 'status': 'FINISHED',
                                                  'end_time': int(time.time() * 1000)})
        return rid

    def row(self, run):
        params = {p['key']: p['value'] for p in run['data'].get('params', [])}
        try:
            value = {k: json.loads(v) for k, v in params.items() if '.' not in k}
        except (ValueError, TypeError):
            value = {}  # Older runs may contain MLflow-truncated JSON parameters.
        if not value or params.get('quivr.record_artifact_only') == 'true':
            value = summary(self.artifact(run, 'record.json'))
        value = summary(value)
        result_key(value)  # mutable remote metadata must never become a filesystem path
        value['metrics'] = {m['key']: m['value'] for m in run['data'].get('metrics', [])}
        return dict(value, run_id=run['info']['run_id'], source='mlflow')


class Results:
    """Log and query measurements. An unset URI records locally for later sync."""
    def __init__(self, directory=None, tracking_uri=None, private_directory=None, private_tracking_uri=None):
        self.directory = pathlib.Path(directory or ROOT / '.scratch/eval/results')
        self.private_directory = pathlib.Path(private_directory or pathlib.Path.home() / '.local/share/quivr/eval-private')
        self.tracking_uri = tracking_uri or os.environ.get('MLFLOW_TRACKING_URI')
        self.private_tracking_uri = private_tracking_uri or os.environ.get('MLFLOW_PRIVATE_TRACKING_URI')
        self.tracking = (Tracking(self.tracking_uri, os.environ.get('MLFLOW_TRACKING_USERNAME'),
                                  os.environ.get('MLFLOW_TRACKING_PASSWORD')) if self.tracking_uri else None)
        private_user, private_password = (os.environ.get('MLFLOW_PRIVATE_TRACKING_USERNAME'),
                                          os.environ.get('MLFLOW_PRIVATE_TRACKING_PASSWORD'))
        self.private_tracking = (Tracking(self.private_tracking_uri, private_user, private_password)
                                 if self.private_tracking_uri and private_user and private_password else None)
        if (self.private_directory.resolve() == self.directory.resolve()
                or self.directory.resolve() in self.private_directory.resolve().parents
                or self.private_directory.resolve() in self.directory.resolve().parents
                or self.private_directory.resolve() == ROOT
                or ROOT in self.private_directory.resolve().parents):
            raise ValueError('private directory must be separate and outside the repository')
        if (self.private_tracking and self.tracking and self.private_tracking.uri == self.tracking.uri
                and self.private_tracking.headers.get('Authorization') == self.tracking.headers.get('Authorization')):
            raise PermissionError('private credentials must differ from the aggregate credentials')

    def log(self, value, baseline=None):
        value = record(value)
        key = value['result_key']
        if baseline:
            other = self.get(baseline, queries=not value['dataset']['private'] or bool(self.private_tracking))
            value['comparison'] = paired_comparison(value, other, private_reader=bool(self.private_tracking))
            for m, stats in value['comparison']['paired'].items():
                value['metrics'][m + '_delta_vs_baseline'] = stats['delta']
                value['metrics'][m + '_p_vs_baseline'] = stats['p_value']
        path = self.directory / (key + '.json')
        if not path.exists():
            if value['dataset']['private'] and value['per_query']:
                save(self.private_directory / (key + '.json'), value)
                value = summary(value)
            save(path, value)
        return self._sync(path)

    def _sync(self, path):
        value = json.loads(path.read_text())
        receipt = {'result_key': value['result_key'], 'status': 'pending'}
        if self.tracking:
            private_path = self.private_directory / (value['result_key'] + '.json')
            needs_private = value['dataset']['private'] and value.get('per_query_status') == 'private'
            if needs_private and not self.private_tracking:
                raise PermissionError('separate private credentials are required to sync private scores')
            try:
                rid = self.tracking.upload(value, value['experiment'])
                if needs_private:
                    private_value = json.loads(private_path.read_text())
                    private_value['aggregate_run_id'] = rid
                    self.private_tracking.upload(private_value, 'private/' + value['experiment'], create=False)
                receipt.update(status='synced', run_id=rid)
            except Unavailable:
                pass
        return receipt

    def sync(self):
        return [self._sync(p) for p in sorted(self.directory.glob('*.json'))]

    def list(self, experiment=None):
        if experiment and experiment.startswith('private/'):
            raise PermissionError('use the private reader for per-query scores')
        cached = {json.loads(p.read_text())['result_key']: json.loads(p.read_text())
                  for p in list(self.directory.glob('*.json')) + list((self.directory / 'cache').glob('*.json'))}
        cached = list(cached.values())
        local = [dict(summary(v), source='local') for v in cached if experiment is None or v['experiment'] == experiment]
        if not self.tracking:
            return local
        try:
            experiments = ([{'experiment_id': self.tracking.experiment(experiment)}] if experiment else
                           self.tracking.request('/api/2.0/mlflow/experiments/search', {'max_results': 1000}).get('experiments', []))
            remote = []
            for exp in experiments:
                if exp.get('name', '').startswith('private/') or not exp['experiment_id']:
                    continue
                for run in self.tracking.runs(exp['experiment_id']):
                    if run['info']['status'] != 'FINISHED':
                        continue
                    row = self.tracking.row(run)
                    if row.get('schema_version') != 1:
                        continue
                    remote.append(row)
                    path = self.directory / 'cache' / (result_key(row) + '.json')
                    if not path.exists():
                        save(path, summary(row))
            keys = {r['result_key'] for r in remote}
            return remote + [r for r in local if r['result_key'] not in keys]
        except Unavailable:
            return local

    def get(self, key, queries=False):
        # A full content key or MLflow run id is accepted.
        matches = [r for r in self.list() if key in (r['result_key'], r.get('run_id'))]
        if len(matches) != 1:
            raise ValueError('result not found or ambiguous')
        value = matches[0]
        if not queries:
            return value
        private = value['dataset']['private']
        if private and not self.private_tracking:
            raise PermissionError('private per-query scores require a separate private reader')
        client = self.private_tracking if private else self.tracking
        if client:
            try:
                eid = client.experiment(('private/' if private else '') + value['experiment'])
                runs = client.runs(eid, value['result_key']) if eid else []
                run = next((r for r in runs if r['info']['status'] == 'FINISHED'), None)
                if run:
                    value['per_query'] = client.artifact(run, 'per_query.json') if value['per_query_status'] != 'missing' else {}
                    return value
                if private:
                    raise PermissionError('private experiment or run is absent or access denied')
            except Unavailable:
                pass
        path = (self.private_directory if private else self.directory) / (value['result_key'] + '.json')
        value['per_query'] = json.loads(path.read_text()).get('per_query', {}) if path.exists() else {}
        return value

    def compare(self, candidate, baseline):
        a, b = self.get(candidate), self.get(baseline)
        permitted = not a['dataset']['private'] or bool(self.private_tracking)
        if permitted and a['dataset']['fingerprint']:
            a, b = self.get(candidate, queries=True), self.get(baseline, queries=True)
        return paired_comparison(a, b, private_reader=bool(self.private_tracking))

    def leaderboard(self, experiment, pareto=None):
        rows = self.list(experiment)
        objectives = {'quality': ('ndcg_at_10', 1), 'cost': ('cost_per_search_usd', -1),
                      'latency': ('latency_p95_ms', -1)}
        if pareto:
            try:
                selected = [objectives[k] for k in pareto.split(',')]
            except KeyError:
                raise ValueError('Pareto objectives: quality,cost,latency') from None
            rows = [r for r in rows if all(r['metrics'].get(m) is not None for m, _ in selected)]
            def group(r):
                return encode([r['dataset'], r['tier'],
                               None if r['dataset']['fingerprint'] else r.get('provenance', {}).get('sha256', r['result_key'])])
            def dominates(a, b):
                av = [a['metrics'][m] * sign for m, sign in selected]
                bv = [b['metrics'][m] * sign for m, sign in selected]
                return group(a) == group(b) and all(x >= y for x, y in zip(av, bv)) and any(x > y for x, y in zip(av, bv))
            rows = [r for r in rows if not any(dominates(a, r) for a in rows)]
        return sorted(rows, key=lambda r: (-(r['metrics'].get('ndcg_at_10') if r['metrics'].get('ndcg_at_10') is not None else -1), r['result_key']))


def paired_comparison(a, b, private_reader=False):
    for field in ('name', 'version', 'split', 'fingerprint', 'private'):
        if a['dataset'][field] != b['dataset'][field]:
            raise ValueError('comparison requires identical dataset lineage')
    if a['tier'] != b['tier']:
        raise ValueError('comparison requires identical measurement tiers')
    if not a['dataset']['fingerprint'] and a.get('provenance', {}).get('sha256') != b.get('provenance', {}).get('sha256'):
        raise ValueError('comparison requires known matching dataset lineage')
    output = {'candidate': a['result_key'], 'baseline': b['result_key'],
              'baseline_run_id': b.get('run_id'),
              'delta': {m: v - b['metrics'][m] for m, v in a['metrics'].items()
                        if v is not None and b['metrics'].get(m) is not None}, 'paired': {}}
    if a['dataset']['private'] and not private_reader:
        output['paired_status'] = 'private: aggregate comparison only'
        return output
    if not a['dataset']['fingerprint']:
        output['paired_status'] = 'unavailable: historical dataset fingerprint missing'
        return output
    aq, bq = a.get('per_query', {}), b.get('per_query', {})
    import scoring
    for metric in sorted(set(aq) & set(bq)):
        if set(aq[metric]) != set(bq[metric]):
            raise ValueError('paired comparison requires identical query IDs')
        output['paired'][metric] = scoring.paired(aq[metric], bq[metric])
    output['paired_status'] = 'available' if output['paired'] else 'unavailable: per-query scores missing'
    output['test'] = scoring.TEST
    return output


_CONFIG_SECRET_PARTS = ('api_key', 'apikey', 'authorization', 'credential', 'password', 'secret')


def _safe_direct_config(value):
    """Keep a candidate plugin config without copying credentials or remote targets."""
    if isinstance(value, dict):
        clean = {}
        for key, item in value.items():
            lowered = str(key).lower()
            if (any(part in lowered for part in _CONFIG_SECRET_PARTS)
                    or lowered in ('token', 'access_token', 'refresh_token', 'bearer_token')
                    or lowered.endswith('_token')):
                continue
            if lowered in ('endpoint', 'base_url', 'url') and isinstance(item, str):
                parsed = urllib.parse.urlsplit(item)
                if (parsed.scheme not in ('http', 'https') or not parsed.netloc
                        or parsed.username or parsed.password or parsed.query or parsed.fragment
                        or parsed.hostname not in ('localhost', '127.0.0.1', '::1')):
                    continue
            clean[key] = _safe_direct_config(item)
        return clean
    if isinstance(value, list):
        return [_safe_direct_config(item) for item in value]
    return value


def _direct_settings(report):
    """Copy direct settings while applying the public-config filter to OSS pins."""
    settings = report.get('settings')
    if not isinstance(settings, dict):
        return settings
    if not isinstance(settings.get('openai_configs'), dict):
        return settings
    copied = dict(settings)
    copied['openai_configs'] = {label: _safe_direct_config(config)
                                for label, config in settings['openai_configs'].items()}
    return copied


def _candidate_config(report, model):
    """Find the exact public candidate pin from the direct report settings."""
    settings = report.get('settings')
    configs = settings.get('openai_configs') if isinstance(settings, dict) else None
    candidate = configs.get(model) if isinstance(configs, dict) else None
    return _safe_direct_config(candidate) if isinstance(candidate, dict) else None


def direct_records(report, experiment, provenance=None):
    """Convert direct bakeoff output, including older aggregate-only evidence."""
    # Preserve frozen legacy aggregate files lacking status/source; modern
    # producers must attest completion before their rows enter shared tracking.
    if report.get('status') != 'complete' and (report.get('status') is not None or report.get('source')):
        raise ValueError('direct evidence must be complete before import')
    provenance = dict(provenance or {})
    source = report.get('source', {})
    historical = not bool(source)
    for model, result in report['results'].items():
        reduced = model.endswith('-1024')
        price_model = model[:-5] if reduced else model
        pooled = 'Cohere-Embed-V5-Pro-1024' in report['results'] and price_model == 'Cohere-Embed-V5-Pro'
        usage = report.get('by_model', {}).get(model, {})
        tokens = usage.get('confirmed_input_tokens', report.get('tokens', {}).get('by_model', {}).get(model))
        usd = usage.get('cost_upper_bound_usd', report.get('usd', {}).get(model))
        meta = dict(provenance)
        if pooled and not usage:
            meta['pooled_cost'] = {'tokens': report.get('tokens', {}).get('by_model', {}).get(price_model),
                                   'usd': report.get('usd', {}).get(price_model),
                                   'models': ['Cohere-Embed-V5-Pro', 'Cohere-Embed-V5-Pro-1024']}
            tokens, usd = None, None
        latency = result.get('latency_ms')
        latency = latency if isinstance(latency, dict) else {}
        serving = result.get('serving')
        serving = serving if isinstance(serving, dict) else None
        metrics = dict(result['mean'], index_seconds=result.get('index_s'),
                       query_encoding_mean_ms=result.get('query_ms'),
                       latency_p50_ms=latency.get('p50'), latency_p95_ms=latency.get('p95'),
                       cost_per_search_usd=None, cost_per_1000_documents_usd=None)
        # Historical total cost includes encoding queries, not just documents.
        indexing = result.get('index_usage', {})
        querying = result.get('query_usage', {})
        if indexing.get('cost_upper_bound_usd') is not None:
            metrics['cost_per_1000_documents_usd'] = indexing['cost_upper_bound_usd'] * 1000 / report['documents']
        if querying.get('cost_upper_bound_usd') is not None:
            metrics['cost_per_search_usd'] = querying['cost_upper_bound_usd'] / report['queries']
        if serving is not None:
            # Serving is a resource estimate, independent of the provider budget.
            # In particular, a zero-priced OSS request is not evidence of free serving.
            if metrics['cost_per_search_usd'] == 0:
                metrics['cost_per_search_usd'] = None
            if metrics['cost_per_1000_documents_usd'] == 0:
                metrics['cost_per_1000_documents_usd'] = None
            metrics['serving_usd_per_million_tokens'] = serving.get('usd_per_million_tokens')
            metrics['serving_estimated_usd'] = serving.get('estimated_usd')
            metrics['cost_per_million_tokens_usd'] = serving.get('usd_per_million_tokens')
            metrics['estimated_serving_cost_usd'] = serving.get('estimated_usd')
        settings = _direct_settings(report)
        candidate = _candidate_config(report, model)
        window_sizes = settings.get('window_chars') if isinstance(settings, dict) else None
        window_chars = result.get('window_chars')
        if window_chars is None and isinstance(window_sizes, dict):
            window_chars = window_sizes.get(model)
        if window_chars is None:
            candidate_model = candidate.get('model', '') if isinstance(candidate, dict) else ''
            window_chars = (1800 if (model == 'multilingual-e5-small (current)'
                                     or model.startswith('multilingual-e5')
                                     or 'e5' in str(candidate_model).lower()) else 6000)
        config = {'model': model, 'dimensions': result['dims'],
                  'chunking': {'window_chars': window_chars,
                               'overlap_chars': 200}, 'candidate_count': 10, 'reranker': None,
                  'fusion_weights': None, 'retrieval': 'exact cosine, best piece, top 10',
                  'settings': settings, 'evidence_sha256': provenance.get('sha256')}
        if candidate is not None:
            config['candidate_config'] = candidate
            if candidate.get('hardware') is not None:
                config['hardware'] = candidate['hardware']
            if candidate.get('model_revision') is not None:
                config['model_revision'] = candidate['model_revision']
        if serving is not None:
            config['hardware'] = serving.get('hardware')
            if serving.get('model_revision') is not None:
                config['model_revision'] = serving['model_revision']
        cost = {'provider': {'tokens': tokens, 'usd': usd, 'modal_seconds': None,
                             'accounting': usage or None}}
        if serving is not None:
            cost['serving'] = dict(serving)
            # The provider budget reserves zero USD for a self-hosted endpoint. Keep
            # that accounting distinct from the measured serving resource estimate.
            if (not usage or (usage.get('confirmed_input_tokens') in (None, 0)
                              and usage.get('cost_upper_bound_usd') in (None, 0))):
                cost['provider']['tokens'] = None
                cost['provider']['usd'] = None
        restricted = (isinstance(report.get('sample'), dict)
                      and report['sample'].get('tier') == 'restricted') or report.get('promotion_eligible') is False
        per_query = {} if restricted else result.get('per_query', {})
        provenance_value = dict(meta, historical=historical, historical_comparison=result.get('vs_current'),
                                **({'latency_ms': dict(latency)} if latency else {}),
                                **({'serving_campaign': dict(report['serving_campaign'])}
                                   if isinstance(report.get('serving_campaign'), dict) else {}),
                                promotion_eligible=report.get('promotion_eligible'),
                                dataset_tier=report.get('sample', {}).get('tier'))
        yield {'schema_version': 1, 'experiment': experiment, 'git_sha': source.get('git_sha'),
               'plugin_digest': source.get('plugin_digest'),
               'config': config,
               'dataset': {'name': report['set'], 'version': report.get('sample', {}).get('version', report.get('fingerprint')),
                           'split': report.get('sample', {}).get('split'), 'fingerprint': report.get('fingerprint'),
                           'private': False}, 'tier': 'direct', 'machine': source.get('machine'),
               'duration_seconds': result.get('duration_seconds'),
               'cost': cost, 'metrics': metrics,
               'per_query': per_query, 'provenance': provenance_value}


def import_evidence(directory, experiment):
    """Read frozen comparison files; never reconstruct historical query arrays."""
    for path in sorted(pathlib.Path(directory).glob('*.json')):
        raw = path.read_bytes()
        yield from direct_records(json.loads(raw), experiment,
                                  {'file': path.name, 'sha256': hashlib.sha256(raw).hexdigest()})


def engine_records(report, experiment, lineage=None, public_sets=()):
    """Convert completed make eval reports; unclassified sets remain private.

    Supplement settings and plugin digest the original report did not capture.
    Never copy raw manifests, errors, URLs or query/document text into the store.
    """
    if report.get('status') != 'completed':
        raise ValueError('engine import requires a completed report')
    lineage = lineage or {}
    if set(lineage) - {'machine', 'plugin_digest', 'config', 'baseline'}:
        raise ValueError('engine lineage accepts machine, plugin_digest, config and baseline')
    if set(public_sets) - set(report['sets']):
        raise ValueError('public set must exist in the engine report')
    run = report['run']
    for name, dataset in report['sets'].items():
        if dataset.get('status') != 'completed':
            raise ValueError('engine import requires completed sets')
        manifest = dataset['manifest']
        for system, measured in dataset['systems'].items():
            accounting = measured.get('accounting', {}).get('scored', {})
            embedding = measured.get('embedding_usage', {})
            indexing = dataset.get('embedding_indexing', {}) if embedding else {}
            cents = accounting.get('cost_cents_per_search')
            embedding_usd = embedding.get('cost_upper_bound_usd')
            per_search = cents / 100 if cents is not None else None
            if embedding:
                per_search = (per_search + embedding_usd / dataset['queries']
                              if per_search is not None and embedding_usd is not None and dataset['queries'] else None)
            cost = {'reranker': {'tokens': accounting.get('actual_input_tokens'),
                                'usd': cents * dataset['queries'] / 100 if cents is not None else None,
                                'modal_seconds': None, 'accounting': accounting},
                    'embedding': {'tokens': (embedding.get('confirmed_input_tokens', 0) + indexing.get('confirmed_input_tokens', 0))
                                  if embedding and indexing else None,
                                  'usd': embedding_usd + indexing['cost_upper_bound_usd']
                                  if embedding_usd is not None and indexing.get('cost_upper_bound_usd') is not None else None,
                                  'modal_seconds': None, 'query': embedding, 'index': indexing}}
            yield {'schema_version': 1, 'experiment': experiment, 'git_sha': run.get('source_revision'),
                   'plugin_digest': lineage.get('plugin_digest'), 'machine': lineage.get('machine'),
                   'duration_seconds': run.get('duration_seconds'), 'tier': 'engine',
                   'config': dict(lineage.get('config', {}), system=system,
                                  profile_version=measured.get('profile_version'),
                                  candidate_count=measured.get('scored_limit'), reranker=measured.get('reranker')),
                   'dataset': {'name': name, 'version': manifest.get('version', manifest.get('fingerprint')),
                               'fingerprint': manifest.get('fingerprint'),
                               'split': manifest.get('split', manifest.get('sample', {}).get('split')),
                               'private': name not in public_sets}, 'cost': cost,
                   'metrics': dict(measured['mean'], latency_p50_ms=measured['latency_ms']['p50'],
                                   latency_p95_ms=measured['latency_ms']['p95'], cost_per_search_usd=per_search,
                                   cost_per_1000_documents_usd=indexing['cost_upper_bound_usd'] * 1000 / dataset['documents']
                                   if indexing.get('cost_upper_bound_usd') is not None and dataset['documents'] else None),
                   'per_query': measured.get('per_query', {}),
                   'provenance': {'source_dirty': run.get('source_dirty'), 'report_run_id': run.get('id'),
                                  'duration_scope': 'entire engine report', 'host': run.get('host'),
                                  'missing_settings': not bool(lineage.get('config'))}}
    if report.get('compare'):
        base = report['compare']
        base_run = dict(run, source_revision=base['source_revision'], source_dirty=False)
        base_lineage = {'machine': lineage.get('machine'), **lineage.get('baseline', {})}
        yield from engine_records({'status': report['status'], 'run': base_run, 'sets': base['sets']},
                                  experiment, base_lineage, public_sets)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', dest='cache_directory', type=pathlib.Path, help='public outbox/cache directory')
    commands = parser.add_subparsers(dest='command', required=True)
    log = commands.add_parser('log', help='log one schema_version=1 JSON record')
    log.add_argument('file', type=pathlib.Path)
    log.add_argument('--baseline', help='compare before logging and store paired statistics')
    listing = commands.add_parser('list', help='list aggregate records')
    listing.add_argument('--experiment')
    compare = commands.add_parser('compare', help='compare two content keys or MLflow run ids')
    compare.add_argument('candidate')
    compare.add_argument('baseline')
    leaderboard = commands.add_parser('leaderboard', help='rank records within their dataset/tier')
    leaderboard.add_argument('--experiment', required=True)
    leaderboard.add_argument('--pareto', help='comma-separated quality,cost,latency objectives')
    commands.add_parser('sync', help='replay the durable local outbox')
    importing = commands.add_parser('import', help='read a directory of frozen direct-comparison JSON files')
    importing.add_argument('evidence_directory', type=pathlib.Path)
    importing.add_argument('--experiment', required=True)
    direct = commands.add_parser('log-direct', help='log a direct_bakeoff.py report')
    direct.add_argument('file', type=pathlib.Path)
    direct.add_argument('--experiment', required=True)
    engine = commands.add_parser('log-engine', help='log a completed make eval report')
    engine.add_argument('file', type=pathlib.Path)
    engine.add_argument('--experiment', required=True)
    engine.add_argument('--lineage', type=pathlib.Path, help='JSON with machine, plugin_digest and full config')
    engine.add_argument('--public-set', action='append', default=[], help='explicitly classify this set as public; repeatable')
    args = parser.parse_args(argv)
    try:
        store = Results(directory=args.cache_directory)
        if args.command == 'log':
            value = json.loads(args.file.read_text())
            output = store.log(value, baseline=args.baseline)
        elif args.command == 'sync':
            output = store.sync()
        elif args.command == 'list':
            output = store.list(args.experiment)
        elif args.command == 'compare':
            output = store.compare(args.candidate, args.baseline)
        elif args.command == 'leaderboard':
            output = store.leaderboard(args.experiment, args.pareto)
        elif args.command == 'import':
            output = [store.log(r) for r in import_evidence(args.evidence_directory, args.experiment)]
        elif args.command == 'log-direct':
            output = [store.log(r) for r in direct_records(json.loads(args.file.read_text()), args.experiment)]
        else:
            lineage = json.loads(args.lineage.read_text()) if args.lineage else None
            output = [store.log(r) for r in engine_records(json.loads(args.file.read_text()), args.experiment,
                                                         lineage, args.public_set)]
        print(encode(output))
        return 0
    except (ValueError, TypeError, AttributeError, KeyError, PermissionError, OSError):
        # Never reflect remote error bodies, filenames or submitted private values.
        print('results: invalid record/configuration, missing local data or access denied', file=__import__('sys').stderr)
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
