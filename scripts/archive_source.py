"""Synthetic source archives on the verification stack's real SeaweedFS.

Only seeds the source dependency. Acceptance reads ingestion, checkpoints,
versions and original bytes through the public Quivr API.
"""
import hashlib
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


def correction_members(count=2000, keys=31, marker=''):
    """Fixed-size ordered revisions, an exact replay, then a late older item."""
    if count < 4 or not 1 <= keys <= count - 2:
        raise ValueError('corrections need at least four members and at most count - 2 keys')
    members, positions, versions = [], {}, {}
    for index in range(count - 2):
        key, position = f'STORY{index % keys}', index + 1
        name = f'2026/01/01/urn%3Aexample%3A{key}-{position}.xml'
        line = f'Synthetic archive {marker}: {key} revision {position}. '.encode()
        body = (line * ((30 << 10) // len(line) + 1))[:30 << 10]
        members.append((name, body))
        positions[key] = str(position)
        versions[key] = versions.get(key, 0) + 1
    members.append(members[0])
    members.append(('2026/01/01/urn%3Aexample%3ASTORY0-0.xml',
                    (f'Synthetic older revision {marker}. '.encode() * (30 << 10))[:30 << 10]))
    versions['STORY0'] += 1
    return members, {'unique_commands': count - 1, 'unique_records': keys,
                     'current_positions': positions, 'versions_per_key': versions}


def seed(stack, bucket='synthetic-archive-source', prefix='inbox/', count=10, marker='', fixture='simple'):
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
    expected = {'unique_commands': count + 3, 'unique_records': count + 2}
    archives = [('2026-01.tar.gz', first, 'tar.gz'), ('2026-02.zip', second, 'zip')]
    total = count + 3
    if fixture == 'corrections':
        members, expected = correction_members(count, min(31, count - 2), marker)
        archives, total = [('2026-01.tar.gz', members, 'tar.gz')], count
    elif fixture != 'simple':
        raise ValueError('unknown archive fixture')
    checksums = {}
    for name, members, kind in archives:
        body = archive_bytes(members, kind)
        checksums[name] = hashlib.sha256(body).hexdigest()
        s3_request(*args, 'PUT', '/' + bucket + '/' + prefix + name, body)
    source = {'bucket': bucket, 'prefix': prefix, 'region': 'us-east-1', 'endpoint': cfg['endpoint'],
              'media_type': 'text/plain', 'member_pattern': '**/*.xml',
              'record_key_pattern': r'urn:example:([A-Z0-9]+)-',
              'source_position_pattern': r'-([0-9]+)\.xml$', 'members_total': total}
    private = stack.directory / 'archive-source.json'
    private.write_text(json.dumps({'config': source, 'credential': {
        'access_key_id': cfg['access_key'], 'secret_access_key': cfg['secret_key']},
        'latest_text': newer.decode(), 'members_total': total,
        'fixture': fixture, 'expected': expected, 'archive_sha256': checksums}) + '\n')
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
