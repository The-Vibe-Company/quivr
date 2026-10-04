"""Weaviate's own resource lines: what names the cause when ingestion stalls on a read-only store (THE-878)."""
import unittest

import resources

# Lines as Weaviate 1.37.15 writes them, shortened; the first with the prefix of docker compose logs.
REFUSED = 'weaviate-1  | {"action":"requests_total","class_name":"QuivrTextV4","error":"put object: store is read-only due to: resource pressure","level":"error","msg":"unexpected error","time":"2026-10-01T11:36:28Z"}'
WARNED = '{"action":"read_disk_use","level":"warning","msg":"disk usage currently at 88.60%, threshold set to 80.00%","path":"/var/lib/weaviate","time":"2026-10-01T10:27:17Z"}'
SHARD = '{"action":"update_shard_status","class":"QuivrTextV4","level":"warning","msg":"shard status changed","prev":"READY","reason":"resource pressure","shard":"wWHhMWNxtm2F","status":"READONLY","time":"2026-10-01T11:20:11Z"}'
SWITCHED = '{"action":"set_shard_read_only","level":"warning","msg":"Set READONLY, disk usage currently at 90.02%, threshold set to 90.00%","path":"/var/lib/weaviate","time":"2026-10-01T11:20:11Z"}'


class ResourceLines(unittest.TestCase):
    def test_the_read_only_switch_names_the_resource_and_refused_writes_are_left_out(self):
        entries = resources.resource_lines([REFUSED, WARNED, 'not json', SHARD, SWITCHED, REFUSED])
        self.assertEqual([e['action'] for e in entries], ['read_disk_use', 'update_shard_status', 'set_shard_read_only'])
        self.assertEqual(entries[1]['status'], 'READONLY')
        self.assertEqual(resources.cause(entries),
                         'Weaviate turned its shards read-only at 2026-10-01T11:20:11Z: Set READONLY, disk usage currently at 90.02%, threshold set to 90.00%')

    def test_no_switch_no_cause(self):
        self.assertIsNone(resources.cause(resources.resource_lines([WARNED, REFUSED])))


if __name__ == '__main__':
    unittest.main()
