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

    def test_missing_source_in_a_later_stage_fails(self):
        root = self.repo('FROM golang AS build\nCOPY go.mod ./\nCOPY cmd ./cmd\nCOPY extra ./extra\n'
                         'FROM python AS plugins\nRUN python -m venv /v \\\n && /v/bin/pip install ./sdk\n'
                         'COPY plugins/moved /app/plugins/moved\n')
        self.assertIn('COPY source plugins/moved does not exist', image_context.check(root, root / 'Dockerfile', GO))

    def test_copied_package_builds(self):
        root = self.repo('FROM golang AS build\nCOPY go.mod ./\nCOPY cmd ./cmd\nCOPY extra ./extra\n')
        self.assertIsNone(image_context.check(root, root / 'Dockerfile', GO))


class WebImageContextTest(unittest.TestCase):
    def repo(self, copy_line):
        root = pathlib.Path(self.enterContext(tempfile.TemporaryDirectory()))
        (root / 'web').mkdir()
        (root / 'web' / 'server.mjs').write_text('import { a } from "./a.mjs";\nimport x from "node:fs";\n')
        (root / 'web' / 'a.mjs').write_text("import { b } from './b.mjs';\n")
        (root / 'web' / 'b.mjs').write_text('export const b = 1;\n')
        # A continued RUN line in the runtime stage must not hide its COPY lines.
        (root / 'Dockerfile').write_text(f'FROM node AS build\nCOPY web ./\nFROM node\nRUN true \\\n && true\n{copy_line}\n')
        return root

    def test_module_missing_from_runtime_stage_fails_and_names_it(self):
        root = self.repo('COPY web/server.mjs web/a.mjs ./')
        failure = image_context.check_web(root, root / 'Dockerfile', entry='web/server.mjs')
        self.assertIn('web/b.mjs', failure)
        self.assertIn('COPY every server module', failure)

    def test_glob_copy_covers_transitive_imports(self):
        root = self.repo('COPY web/*.mjs ./')
        self.assertIsNone(image_context.check_web(root, root / 'Dockerfile', entry='web/server.mjs'))


if __name__ == '__main__':
    unittest.main()
