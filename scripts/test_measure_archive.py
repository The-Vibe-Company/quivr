"""Archive measurement configuration and fixture counts use public inputs."""
import hashlib
import io
import json
import pathlib
import re
import tarfile
import tempfile
import types
import unittest
from unittest.mock import patch
from urllib.parse import unquote

import archive_source
import measure_archive


class ArchiveMeasurement(unittest.TestCase):
    def test_omitted_and_explicit_concurrency(self):
        fixture = {'config': {'bucket': 'example-archives', 'prefix': 'inbox/'}}
        for value in (None, 1, 8):
            with self.subTest(concurrency=value):
                argv = ['--stack', 'local', '--fixture', 'corrections', '--items', '2000']
                if value is not None:
                    argv += ['--concurrency', str(value)]
                args = measure_archive._arguments(argv)
                config = measure_archive._connector_config(fixture, args.batch_size, args.concurrency)
                expected = {'bucket': 'example-archives', 'prefix': 'inbox/', 'batch_size': 100}
                if value is not None:
                    expected['concurrency'] = value
                self.assertEqual(config, expected)

    def test_rate_labels_preserve_members_and_distinguish_unique_commands(self):
        self.assertEqual(measure_archive._acquisition_rates(4, 3, 2), {
            'accepted_members_per_second': 2.0,
            'accepted_commands_per_second': 1.5,
            'traversed_members_per_second': 2.0})
        self.assertEqual(measure_archive._acquisition_rates(4, 3, None), {
            'accepted_members_per_second': 0,
            'accepted_commands_per_second': 0,
            'traversed_members_per_second': 0})

    def test_corrections_fixture_expected_commands_and_positions(self):
        # Exercise the seeder's actual compressed source bytes. Only S3 is faked;
        # an independent archive reader counts identities rather than trusting
        # a count produced by the fixture builder.
        with tempfile.TemporaryDirectory() as directory:
            stack = types.SimpleNamespace(directory=pathlib.Path(directory))
            (stack.directory / 'config.json').write_text(json.dumps({'s3': {
                'endpoint': 'http://storage.example', 'access_key': 'synthetic-reader',
                'secret_key': 'synthetic-secret'}}))
            uploads = []
            with patch('archive_source.s3_request', side_effect=lambda *args: uploads.append(args)):
                private = archive_source.seed(stack, count=6, marker='fixed', fixture='corrections')
            fixture = json.loads(private.read_text())
            body = uploads[-1][-1]
            with tarfile.open(fileobj=io.BytesIO(body), mode='r:gz') as archive:
                members = [(unquote(m.name), archive.extractfile(m).read()) for m in archive]
            self.assertEqual(len(members), 6)
            self.assertEqual(members[0], members[-2])
            self.assertTrue(all(len(data) == 30 << 10 for _, data in members))
            commands = {(name, hashlib.sha256(data).hexdigest()) for name, data in members}
            self.assertEqual(len(commands), 5)
            self.assertEqual(fixture['expected'], {
                'unique_commands': 5, 'unique_records': 4,
                'current_positions': {'STORY0': '1', 'STORY1': '2', 'STORY2': '3', 'STORY3': '4'},
                'versions_per_key': {'STORY0': 2, 'STORY1': 1, 'STORY2': 1, 'STORY3': 1}})
            self.assertEqual(fixture['members_total'], 6)
            self.assertEqual(fixture['archive_sha256']['2026-01.tar.gz'], hashlib.sha256(body).hexdigest())
            self.assertEqual(re.search(r'-([0-9]+)\.xml$', members[-1][0]).group(1), '0')


if __name__ == '__main__':
    unittest.main()
