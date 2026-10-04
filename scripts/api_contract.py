"""Check test responses against their authoritative public HTTP operation."""
import functools
import importlib.util
import pathlib
import re
import urllib.parse

from jsonschema import Draft202012Validator


@functools.lru_cache(maxsize=1)
def _contract():
    source = pathlib.Path(__file__).resolve().parents[1] / 'contracts/http/v0/bundle.py'
    spec = importlib.util.spec_from_file_location('quivr_contract_bundle', source)
    bundle = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(bundle)
    return bundle.load()


@functools.lru_cache(maxsize=None)
def _validator(route, method, response_status, media):
    doc = _contract()
    schema = ({'$ref': '#/components/schemas/Error'} if route is None else
              doc['paths'][route][method]['responses'][response_status]['content'][media]['schema'])
    return Draft202012Validator({**schema, 'components': doc['components']})


def check_response(method, path, status, body, content_type='application/json', headers=None):
    """Raise AssertionError for drift; return the body for compact fake callers."""
    prefix = f'{method.upper()} {path} HTTP {status}'
    if status == 204 and body not in (None, '', b''):
        raise AssertionError(f'{prefix}: HTTP 204 cannot carry a response body')
    doc = _contract()
    route = urllib.parse.urlsplit(path).path
    if route not in doc['paths']:
        # The connector API's terminal {path} includes nested route segments.
        matches = [pattern for pattern in doc['paths']
                   if re.fullmatch('/'.join('.+' if part == '{path}' and i == len(pattern.split('/')) - 1
                                           else '[^/]+' if part.startswith('{') else re.escape(part)
                                           for i, part in enumerate(pattern.split('/'))), route)]
        if len(matches) != 1:
            if not matches and status == 404:
                return _check_body(prefix, None, '', '', body, content_type)
            raise AssertionError(f'{prefix}: expected one contract route, got {matches}')
        route = matches[0]
    operation = doc['paths'][route].get(method.lower())
    if operation is None:
        if status == 405:
            return _check_body(prefix, None, '', '', body, content_type)
        raise AssertionError(f'{prefix}: method absent from contract for {route}')
    responses = operation['responses']
    key = str(status) if str(status) in responses else f'{status // 100}XX'
    if key not in responses:
        key = 'default'
    if key not in responses:
        raise AssertionError(f'{prefix}: status absent from contract')
    variant = operation.get('x-quivr-plugin-response')
    if variant:
        marker = next((value for name, value in (headers or {}).items()
                       if name.lower() == variant['header'].lower()), None)
        if marker is not None:
            if marker != variant['value']:
                raise AssertionError(f"{prefix}: invalid {variant['header']} response variant")
            if str(status) not in variant['statuses'] and f'{status // 100}XX' not in variant['statuses']:
                raise AssertionError(f'{prefix}: status absent from plugin response contract')
            return body  # This operation declares the marked reply plugin-owned.
    content = responses[key].get('content', {})
    if not content:
        # Webhook responses explicitly leave the body and media to the plugin.
        return body
    media = content_type.split(';', 1)[0].strip().lower()
    if media not in content:
        raise AssertionError(f'{prefix}: media {media!r} absent from contract; expected {list(content)}')
    return _check_body(prefix, route, method.lower(), key, body, media)


def _check_body(prefix, route, method, key, body, content_type):
    media = content_type.split(';', 1)[0].strip().lower()
    if route is None and media != 'application/json':
        raise AssertionError(f'{prefix}: router Error must be application/json')
    error = next(_validator(route, method, key, media).iter_errors(body), None)
    if error is not None:
        raise AssertionError(f'{prefix}: {route} response {key}: {error.message}') from error
    return body


def checked_fake(call):
    """Validate every result of a fake Client.call, including early returns."""
    @functools.wraps(call)
    def checked(self, method, path, *args, **kwargs):
        status, body = call(self, method, path, *args, **kwargs)
        check_response(method, path, status, body)
        return status, body
    return checked
