"""Synthetic source archives on the verification stack's real SeaweedFS.

Only seeds the source dependency. Acceptance reads ingestion, checkpoints,
versions and original bytes through the public Quivr API.
"""
import gzip
import io
import json
import pathlib
import tarfile
import zipfile
import uuid


from deploy.reset_storage import s3_request


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
