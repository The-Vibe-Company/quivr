import os
import pathlib
import tempfile
import unittest

import image_context

GO = os.environ.get('GO', 'go')
MAIN = 'package main\n\nimport "example.test/app/extra"\n\nfunc main() { extra.Run() }\n'


class ImageContextTest(unittest.TestCase):
    def repo(self, dockerfile):
        root = pathlib.Path(self.enterContext(tempfile.TemporaryDirectory()))
        (root / 'go.mod').write_text('module example.test/app\n\ngo 1.21\n')
        (root / 'cmd' / 'quivr').mkdir(parents=True)
        (root / 'cmd' / 'quivr' / 'main.go').write_text(MAIN)
        (root / 'extra').mkdir()
        (root / 'extra' / 'extra.go').write_text('package extra\n\nfunc Run() {}\n')
        (root / 'Dockerfile').write_text(dockerfile)
        return root

    def test_missing_package_fails_and_names_the_fix(self):
        root = self.repo('FROM golang AS build\nCOPY go.mod ./\nCOPY cmd ./cmd\nFROM scratch\nCOPY extra ./extra\n')
        failure = image_context.check(root, root / 'Dockerfile', GO)
        self.assertIn('example.test/app/extra', failure)
        self.assertIn('COPY every top-level Go package', failure)

    def test_copied_package_builds(self):
        root = self.repo('FROM golang AS build\nCOPY go.mod ./\nCOPY cmd ./cmd\nCOPY extra ./extra\n')
        self.assertIsNone(image_context.check(root, root / 'Dockerfile', GO))


if __name__ == '__main__':
    unittest.main()
