"""Inventory the dedicated engine bucket before replacing its storage."""
import datetime
import hashlib
import hmac
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

def s3_request(endpoint, access, secret, method, path, body=b''):
    """Minimal signed S3 operations with sanitized failures."""
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
        raise RuntimeError(f'Object store {method} failed') from None



def inventory(config):
    args = config['endpoint'], config['access_key'], config['secret_key']
    root = ET.fromstring(s3_request(*args, 'GET', '/'))
    names = [entry.text for entry in root.findall('.//{*}Buckets/{*}Bucket/{*}Name')]
    if names != [config['bucket']]:
        raise RuntimeError('storage contains other buckets; source archives must stay outside reset storage')
    contents = ET.fromstring(s3_request(*args, 'GET', '/' + config['bucket']))
    return {'bucket': config['bucket'], 'objects_at_least': len(contents.findall('{*}Contents')),
            'truncated': contents.findtext('{*}IsTruncated', 'false') == 'true'}
