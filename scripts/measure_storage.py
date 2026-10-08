#!/usr/bin/env python3
"""Read aggregate PostgreSQL storage without exporting documents or credentials.

Run on identical isolated samples before and after matched imports.
Physical allocation includes retained historical audit rows and dead tuples.
Object counts cover referenced files; this never enumerates or deletes S3 keys.
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import subprocess
import socket
import re
import urllib.parse


SQL = """
BEGIN READ ONLY;
SET LOCAL statement_timeout='30s';
SELECT json_build_object(
 'database_bytes',pg_database_size(current_database()),
 'documents',(SELECT count(*) FROM records),
 'versions',(SELECT count(*) FROM record_versions),
 'tables',(SELECT json_agg(x ORDER BY x.name) FROM (
   SELECT c.relname AS name,c.reltuples::bigint AS estimated_rows,
     pg_relation_size(c.oid) AS heap_bytes,
     pg_table_size(c.oid)-pg_relation_size(c.oid) AS toast_and_auxiliary_bytes,
     pg_indexes_size(c.oid) AS index_bytes,
     pg_total_relation_size(c.oid) AS total_bytes,
     coalesce(st.n_dead_tup,0) AS estimated_dead_rows
   FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   LEFT JOIN pg_stat_user_tables st ON st.relid=c.oid
   WHERE n.nspname='public' AND c.relkind='r'
 ) x),
 'vector_files',(SELECT count(*) FROM embedding_files),
 'compact_vectors',(SELECT count(*) FROM compact_embeddings),
 'compact_coverage_rows',(SELECT count(*) FROM compact_embedding_coverage),
 'captured_request_copies',(SELECT count(*) FROM ingestion_receipts WHERE octet_length(canonical_request)>0),
 'normalization_outcome_objects',(SELECT count(*) FROM normalizations WHERE outcome_key IS NOT NULL),
 'average_compact_vector_column_bytes',(SELECT avg(pg_column_size(organization_id)+pg_column_size(segment_id)+pg_column_size(space_id)+pg_column_size(file_id)+pg_column_size(ordinal)+pg_column_size(vector_sha256)+pg_column_size(artifact_sha256)) FROM compact_embeddings)
);
COMMIT;
"""


def connection_environment(cfg: dict) -> dict:
    url = urllib.parse.urlsplit(cfg['database_url'])
    if url.scheme not in ('postgres', 'postgresql') or not url.hostname:
        raise ValueError('unsupported database configuration')
    # Credentials go through the child environment, never argv or report text.
    child_env = dict(os.environ)
    for variable in ('PGSSLMODE', 'PGSSLROOTCERT', 'PGSSLCERT', 'PGSSLKEY',
                     'PGHOSTADDR', 'PGSSLMINPROTOCOLVERSION'):
        child_env.pop(variable, None)
    child_env.update({
        'PGHOST': url.hostname, 'PGPORT': str(url.port or 5432),
        'PGUSER': urllib.parse.unquote(url.username or ''),
        'PGPASSWORD': urllib.parse.unquote(url.password or ''),
        'PGDATABASE': urllib.parse.unquote(url.path.lstrip('/')),
    })
    parameters = urllib.parse.parse_qs(url.query)
    for field, variable in [('sslmode', 'PGSSLMODE'), ('sslrootcert', 'PGSSLROOTCERT'),
                            ('sslcert', 'PGSSLCERT'), ('sslkey', 'PGSSLKEY')]:
        if field in parameters:
            child_env[variable] = parameters[field][-1]
    tls = cfg.get('tls', {}).get('postgres', {})
    settings = ('ca_file', 'cert_file', 'key_file', 'server_name')
    if 'enabled' in tls or any(tls.get(key) for key in settings):
        enabled = tls.get('enabled', True)
        if type(enabled) is not bool or (not enabled and any(tls.get(key) for key in settings)):
            raise ValueError('invalid PostgreSQL TLS policy')
        child_env['PGSSLMODE'] = 'verify-full' if enabled else 'disable'
        for key, variable in [('ca_file', 'PGSSLROOTCERT'), ('cert_file', 'PGSSLCERT'), ('key_file', 'PGSSLKEY')]:
            child_env.pop(variable, None)
            if tls.get(key):
                child_env[variable] = tls[key]
        name = tls.get('server_name')
        if name:
            if not re.fullmatch(r'[A-Za-z0-9.:-]+', name):
                raise ValueError('invalid PostgreSQL TLS server name')
            # libpq verifies PGHOST but connects to PGHOSTADDR. Preserve the
            # configured endpoint while honoring a certificate name override.
            address = socket.getaddrinfo(url.hostname, url.port or 5432, type=socket.SOCK_STREAM)[0][4][0]
            child_env['PGHOSTADDR'] = address
            child_env['PGHOST'] = name
    if child_env.get('PGSSLMODE') not in ('verify-full', 'disable'):
        raise ValueError('PostgreSQL requires verified TLS or explicit plaintext')
    if bool(child_env.get('PGSSLCERT')) != bool(child_env.get('PGSSLKEY')):
        raise ValueError('PostgreSQL TLS certificate and key must be paired')
    child_env['PGSSLMINPROTOCOLVERSION'] = 'TLSv1.2'
    return child_env


def snapshot(config: Path) -> dict:
    child_env = connection_environment(json.loads(config.read_text()))
    result = subprocess.run(['psql', '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1'],
                            input=SQL, text=True, env=child_env,
                            capture_output=True, timeout=40, check=False)
    if result.returncode:
        raise RuntimeError('storage snapshot failed; check database access and statement budget')
    start, end = result.stdout.find('{'), result.stdout.rfind('}')
    if start < 0 or end < start:
        raise RuntimeError('storage snapshot returned no aggregate')
    report = json.loads(result.stdout[start:end + 1])
    report['captured_at'] = dt.datetime.now(dt.timezone.utc).isoformat()
    total = sum(table['total_bytes'] for table in report['tables'])
    report['public_table_bytes'] = total
    report['public_table_bytes_per_document'] = total / report['documents'] if report['documents'] else None
    report['database_bytes_per_document'] = report['database_bytes'] / report['documents'] if report['documents'] else None
    report['limits'] = ['Physical sizes include retained audit detail, dead tuples and empty table/index allocation.',
                        'Run ANALYZE after matched imports for row estimates; compare identical samples and counts.',
                        'Referenced file count excludes retained physical legacy and superseded S3 objects.',
                        'Import throughput and search quality require their own matched workload measurements.']
    return report


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, default=os.environ.get('QUIVR_CONFIG'))
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.config is None:
        parser.error('--config or QUIVR_CONFIG is required')
    try:
        report = snapshot(args.config)
    except (OSError, ValueError, KeyError, RuntimeError, StopIteration, subprocess.TimeoutExpired):
        parser.exit(1, 'storage snapshot failed; no document or credential data exported\n')
    args.output.write_text(json.dumps(report, indent=2) + '\n')
    print(f'Storage snapshot saved; documents={report["documents"]}, bytes/document={report["public_table_bytes_per_document"]}')


if __name__ == '__main__':
    main()
