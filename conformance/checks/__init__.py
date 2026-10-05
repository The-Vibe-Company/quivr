"""Maintainer-owned checks. Cases select data, never commands or Python imports."""
import hashlib
import json
import math
import os
import pathlib
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from typing import Optional

LIMIT = 4 * 1024 * 1024


class Skip(Exception):
    """The runner lacks an input needed to measure a requirement."""


@dataclass
class Observation:
    met: bool
    measurement: object
    evidence: dict
    reason: str = ''


def read_bytes(path):
    with pathlib.Path(path).open('rb') as source:
        data = source.read(LIMIT + 1)
    if len(data) > LIMIT:
        raise ValueError('evidence exceeds 4 MiB; supply a bounded capture')
    return data


def digest(data):
    return hashlib.sha256(data).hexdigest()


def parse_json(raw):
    def refuse_constant(value):
        raise ValueError('non-standard JSON constant')
    value = json.loads(raw, parse_constant=refuse_constant)
    def finite(item):
        if isinstance(item, float) and not math.isfinite(item):
            raise ValueError('non-finite JSON number')
        if isinstance(item, dict):
            for child in item.values():finite(child)
        elif isinstance(item, list):
            for child in item:finite(child)
    finite(value)
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


@dataclass
class Context:
    api_url: str = ''
    probe_url: str = ''
    api_key: str = field(default='', repr=False)
    logs: dict = field(default_factory=dict)
    binary: Optional[pathlib.Path] = None
    image: str = ''
    sbom: Optional[pathlib.Path] = None
    openapi: Optional[pathlib.Path] = None
    evidence_dir: Optional[pathlib.Path] = None
    timeout: float = 5

    def request(self, parameters, path=None):
        base = self.probe_url if parameters.get('target', 'api') == 'probe' else self.api_url
        if not base:
            raise Skip('no ' + parameters.get('target', 'api') + ' URL supplied')
        route = path or parameters['path']
        # Defense in depth: never let a route replace the trusted operator-selected target.
        if not route.startswith('/') or route.startswith('//') or '\\' in route:
            raise ValueError('path must be a relative HTTP route')
        headers = {}
        if parameters.get('authenticated'):
            if not self.api_key:
                raise Skip('authenticated probe needs QUIVR_CONFORMANCE_API_KEY')
            headers['Authorization'] = 'Bearer ' + self.api_key
        request = urllib.request.Request(base.rstrip('/') + route, headers=headers)
        start = time.monotonic()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        try:
            response = opener.open(request, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read(LIMIT + 1)
            if len(data) > LIMIT:
                raise ValueError('HTTP evidence exceeds 4 MiB')
            return response.code, response.headers, data, round((time.monotonic() - start) * 1000, 3)


def http_probe(context, parameters, threshold):
    status, _, body, elapsed = context.request(parameters)
    measurement = {'status': status, 'latency_ms': elapsed}
    return Observation(status == threshold['status'] and elapsed <= threshold['max_latency_ms'],
                       measurement, {'body_sha256': digest(body), 'path': parameters['path'],
                                     'target': parameters.get('target', 'api')})


def openapi_valid(context, parameters, threshold):
    if not context.openapi:
        raise Skip('no OpenAPI contract supplied for this target version')
    # The trusted contract bundles local shared schemas; cases cannot select $ref URLs.
    import yaml
    from openapi_spec_validator import validate
    raw = read_bytes(context.openapi)
    spec = yaml.safe_load(raw)
    # Bundle Quivr's split local contract using its source-owned helper when present.
    bundle = context.openapi.parent / 'bundle.py'
    if bundle.exists():
        import importlib.util
        module_spec = importlib.util.spec_from_file_location('conformance_openapi_bundle', bundle)
        module = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(module)
        spec = module.load()
    try:
        validate(spec)
        valid = True
    except Exception as error:
        # Do not publish contract values embedded in a validator exception.
        valid = False
        return Observation(False, {'valid': False}, {'contract_sha256': digest(raw),
                           'validation_error': type(error).__name__}, 'OpenAPI validation failed')
    return Observation(valid, {'valid': valid}, {'contract_sha256': digest(raw), 'file': str(context.openapi)})


def metric_exposed(context, parameters, threshold):
    status, _, body, _ = context.request({'target': 'probe'}, '/metrics')
    name = re.escape(parameters['name'])
    text = body.decode('utf-8')
    types = re.findall(r'^# TYPE ' + name + r' (\w+)\s*$', text, re.MULTILINE)
    suffix = ''
    if threshold['type'] == 'histogram':
        suffix = r'(?:_count|_sum|_bucket(?=\{(?:[^\n]*,)?[ \t]*le="))'
    elif threshold['type'] == 'summary':
        suffix = r'(?:_count|_sum|(?=\{(?:[^\n]*,)?[ \t]*quantile="))'
    samples = re.findall(r'^' + name + suffix + r'(?:\{[^\n]*\})?[ \t]+(?:[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?|[+-]?Inf|NaN)(?:[ \t]|$)', text, re.MULTILINE)
    return Observation(status == 200 and types == [threshold['type']] and bool(samples),
                       {'status': status, 'types': types, 'samples': len(samples)},
                       {'body_sha256': digest(body), 'path': '/metrics'})


def log_format(context, parameters, threshold):
    stream = parameters['stream']
    if stream not in context.logs:
        raise Skip('no separate ' + stream + ' capture supplied')
    raw = read_bytes(context.logs[stream])
    lines = raw.splitlines()
    invalid = []
    for index, line in enumerate(lines, 1):
        try:
            value = parse_json(line)
            if not isinstance(value, dict) or any(name not in value for name in threshold['fields']):
                invalid.append(index)
        except (ValueError, UnicodeDecodeError):
            invalid.append(index)
    return Observation(bool(lines) and not invalid, {'records': len(lines), 'invalid_lines': invalid},
                       {'stream': stream, 'file': str(context.logs[stream]), 'sha256': digest(raw)},
                       '' if lines else 'expected stream has no records')


def image_property(context, parameters, threshold):
    if not context.image:
        raise Skip('no image selected; host binaries do not prove image properties')
    inspection = subprocess.run(['docker', 'image', 'inspect', context.image], check=True,
                                capture_output=True, timeout=30)
    image = parse_json(inspection.stdout)[0]
    config = image['Config']
    user = config.get('User', '').split(':')[0]
    if threshold.get('non_root') and user and user != 'root' and not user.isdigit():
        raise Skip('named image user needs an effective UID check; inspect cannot prove non-root')
    non_root = user.isdigit() and int(user) > 0
    labels = config.get('Labels') or {}
    measurement = {'user': user, 'non_root': non_root,
                   'labels_present': {name: bool(labels.get(name)) for name in threshold.get('labels', [])}}
    evidence = {'image_id': image['Id'], 'inspection_sha256': digest(inspection.stdout)}
    met = (not threshold.get('non_root') or non_root) and all(measurement['labels_present'].values())
    if threshold.get('sbom'):
        if not context.sbom:
            raise Skip('no SBOM supplied for the selected image')
        raw = read_bytes(context.sbom)
        document = parse_json(raw)
        # An image-bound Syft inventory names the exact inspected image id in its source.
        bound = document.get('source', {}).get('target', {})
        bound_id = bound.get('imageID') if isinstance(bound, dict) else None
        present = bool(document.get('artifacts')) and bound_id == image['Id']
        measurement['sbom_present'] = present
        evidence.update(sbom_sha256=digest(raw), sbom_file=str(context.sbom), sbom_image_id=bound_id)
        met = met and present
    return Observation(met, measurement, evidence)


def error_shape(context, parameters, threshold):
    status, headers, raw, _ = context.request(parameters)
    expected = threshold['fields']
    try:
        value = parse_json(raw)
    except (ValueError, UnicodeDecodeError):
        value = None
    def matches(value, kind):
        if kind == 'number':return type(value) in (int, float)
        types = {'string': str, 'boolean': bool, 'object': dict, 'array': list, 'null': type(None)}
        return type(value) is types[kind] and (kind != 'string' or bool(value))
    fields = {name: isinstance(value, dict) and name in value and matches(value[name], kind)
              for name, kind in expected.items()}
    content_type = headers.get('Content-Type', '').split(';')[0].strip().lower()
    met = status == threshold['status'] and content_type == 'application/json' and all(fields.values())
    return Observation(met, {'status': status, 'content_type': content_type, 'fields_valid': fields},
                       {'body_sha256': digest(raw), 'path': parameters['path']})


INVALID_CONFIGS = {'unknown_field': b'{"conformance_unknown_field":true}',
                   'invalid_json': b'{', 'missing_required': b'{}'}


def config_refuses_invalid(context, parameters, threshold):
    if not context.binary:
        raise Skip('no target quivr binary supplied')
    with tempfile.TemporaryDirectory(prefix='invalid-config-', dir=context.evidence_dir) as directory:
        directory = pathlib.Path(directory)
        config = directory / 'config.json'
        config.write_bytes(INVALID_CONFIGS[parameters['fixture']])
        # No operator configuration, provider secrets or paid-service addresses are inherited.
        with (directory / 'stdout').open('wb') as stdout, (directory / 'stderr').open('wb') as stderr:
            result = subprocess.run([str(context.binary.resolve()), 'api'],
                                    env={'PATH': os.environ.get('PATH', ''), 'QUIVR_CONFIG': str(config)},
                                    stdout=stdout, stderr=stderr, timeout=context.timeout)
        raw = read_bytes(directory / 'stderr')
        diagnostic_found = threshold['diagnostic'] in raw.decode('utf-8', errors='replace')
        return Observation(result.returncode == threshold['exit_code'] and diagnostic_found,
                           {'exit_code': result.returncode, 'diagnostic_found': diagnostic_found},
                           {'stderr_sha256': digest(raw), 'fixture': parameters['fixture']})


CHECKS = {'http_probe': http_probe, 'openapi_valid': openapi_valid, 'metric_exposed': metric_exposed,
          'log_format': log_format, 'image_property': image_property, 'error_shape': error_shape,
          'config_refuses_invalid': config_refuses_invalid}
