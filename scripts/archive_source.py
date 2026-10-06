"""Synthetic source archives on the verification stack's real SeaweedFS.

Only seeds the source dependency. Acceptance reads ingestion, checkpoints,
versions and original bytes through the public Quivr API.
"""
import datetime
import gzip
import hashlib
import hmac
import io
import json
import pathlib
import tarfile
import urllib.parse
import urllib.request
import zipfile
import uuid


def s3_request(endpoint, access, secret, method, path, body=b''):
    """Small SigV4 client for fixture PUTs, with no credential logging."""
    now = datetime.datetime.now(datetime.timezone.utc)
    stamp, day = now.strftime('%Y%m%dT%H%M%SZ'), now.strftime('%Y%m%d')
    host = urllib.parse.urlsplit(endpoint).netloc
    uri = urllib.parse.quote(path, safe='/')
    digest = hashlib.sha256(body).hexdigest()
    headers = f'host:{host}\nx-amz-content-sha256:{digest}\nx-amz-date:{stamp}\n'
    signed = 'host;x-amz-content-sha256;x-amz-date'
    canonical = '\n'.join((method, uri, '', headers, signed, digest))
    scope = f'{day}/us-east-1/s3/aws4_request'
    message = '\n'.join(('AWS4-HMAC-SHA256', stamp, scope, hashlib.sha256(canonical.encode()).hexdigest()))
    key = ('AWS4' + secret).encode()
    for value in (day, 'us-east-1', 's3', 'aws4_request'):
        key = hmac.new(key, value.encode(), hashlib.sha256).digest()
    signature = hmac.new(key, message.encode(), hashlib.sha256).hexdigest()
    request = urllib.request.Request(endpoint + uri, data=body, method=method, headers={
        'x-amz-date': stamp, 'x-amz-content-sha256': digest,
        'Authorization': f'AWS4-HMAC-SHA256 Credential={access}/{scope}, SignedHeaders={signed}, Signature={signature}'})
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return response.read()
    except Exception:
        # An HTTP exception's request may include capabilities. Surface only
        # the dependency action, never the signed request or credential.
        raise RuntimeError(f'Synthetic S3 source {method} failed') from None


def archive_bytes(members, kind):
    raw = io.BytesIO()
    if kind == 'zip':
        with zipfile.ZipFile(raw, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
            for name, data in members:
                archive.writestr(name, data)
        return raw.getvalue()
    with tarfile.open(fileobj=raw, mode='w') as archive:
        for name, data in members:
            entry = tarfile.TarInfo(name)
            entry.size = len(data)
            archive.addfile(entry, io.BytesIO(data))
    return gzip.compress(raw.getvalue(), mtime=0)


def seed(stack, bucket='synthetic-archive-source', prefix='inbox/', count=10, marker=''):
    cfg = json.loads((stack.directory / 'config.json').read_text())['s3']
    args = (cfg['endpoint'], cfg['access_key'], cfg['secret_key'])
    # Each isolated verification stack owns its bucket, created exactly once.
    s3_request(*args, 'PUT', '/' + bucket)
    suffix = (' ' + marker).encode() if marker else b''
    newer = b'Harbourlight latest revision twelve. The ferry service has resumed.' + suffix
    older = b'Harbourlight earlier revision three. The ferry service is suspended.' + suffix
    first = [('2026/01/01/10/urn%3Aexample%3AITEM7-12.xml', newer)]
    first += [(f'2026/01/01/10/urn%3Aexample%3AFILLER{i}-1.xml',
               f'Harbourlight synthetic bulletin {i}.'.encode() + suffix) for i in range(count)]
    second = [('2026/01/02/11/urn%3Aexample%3AITEM7-3.xml', older),
              ('2026/01/03/12/urn%3Aexample%3AITEM9-1.xml', b'Harbourlight final bulletin.' + suffix)]
    for name, members, kind in [('2026-01.tar.gz', first, 'tar.gz'), ('2026-02.zip', second, 'zip')]:
        s3_request(*args, 'PUT', '/' + bucket + '/' + prefix + name, archive_bytes(members, kind))
    source = {'bucket': bucket, 'prefix': prefix, 'region': 'us-east-1', 'endpoint': cfg['endpoint'],
              'media_type': 'text/plain', 'member_pattern': '**/*.xml',
              'record_key_pattern': r'urn:example:([A-Z0-9]+)-',
              'source_position_pattern': r'-([0-9]+)\.xml$', 'members_total': count + 3}
    private = stack.directory / 'archive-source.json'
    private.write_text(json.dumps({'config': source, 'credential': {
        'access_key_id': cfg['access_key'], 'secret_access_key': cfg['secret_key']},
        'latest_text': newer.decode(), 'members_total': count + 3}) + '\n')
    private.chmod(0o600)
    return private


def verify(stack):
    import connector_plugin
    private = seed(stack,bucket="synthetic-archive-source-"+uuid.uuid4().hex[:10])
    # Stack.tests already supplies all normal scoped acceptance keys.
    env={'QUIVR_TEST_ARCHIVE_SOURCE':str(private)}
    stack.tests('^TestArchiveSourceBeforeRestart$',extra_env=env)
    connector_plugin.stop_first_party(stack, only=['object-storage-archive'])
    stack.stop_worker()
    connector_plugin.start_first_party(stack, only=['object-storage-archive'])
    stack.start_worker()
    stack.tests('^TestArchiveSourceAfterRestart$',extra_env=env)
