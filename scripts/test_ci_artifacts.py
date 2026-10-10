"""Failure evidence remains bounded even when a test writes huge logs."""
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

import ci_artifacts


class Bundle(unittest.TestCase):
    def test_caps_evidence_and_never_reads_symlinked_or_configuration_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence = root / 'evidence'
            evidence.mkdir()
            (evidence / 'report.json').write_text('{"failed_step":"owner"}')
            (evidence / 'worker.log').write_bytes(b'x' * 4096 + b'failure at end\n')
            (evidence / 'config.json').write_text('private configuration')
            (root / 'outside.log').write_text('outside content')
            (evidence / 'linked.log').symlink_to(root / 'outside.log')
            for index in range(10):
                (evidence / f'extra-{index}.log').write_text('extra')
            output = root / 'bundle.tar.gz'
            ci_artifacts.bundle([evidence], output, max_files=3, max_bytes=1024, file_bytes=256)
            with tarfile.open(output) as archive:
                files = [m for m in archive.getmembers() if m.isfile()]
                self.assertLessEqual(len(files), 4)  # Three evidence members plus bounded index.
                self.assertLessEqual(sum(m.size for m in files if m.name != 'index.json'), 1024)
                content = {m.name: archive.extractfile(m).read() for m in files}
            self.assertTrue(any(name.endswith('report.json') for name in content))
            self.assertTrue(any(b'failure at end' in value for value in content.values()))
            self.assertFalse(any(b'outside content' in value or b'private configuration' in value for value in content.values()))
            index = json.loads(content['index.json'])
            self.assertTrue(index['truncated'])
            self.assertGreater(index['omitted'], 0)


if __name__ == '__main__':
    unittest.main()
