import os
import pathlib
import tempfile
import unittest
import zipfile

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


class ConnectorPluginImageContextTest(unittest.TestCase):
    """A Go connector plugin under plugins/<id> builds from exactly the connectors stage's COPY sources."""
    BUILD = 'FROM golang AS build\nCOPY go.mod ./\nCOPY cmd ./cmd\nCOPY extra ./extra\n'

    def repo(self, connectors_stage):
        root = pathlib.Path(self.enterContext(tempfile.TemporaryDirectory()))
        (root / 'go.mod').write_text('module example.test/app\n\ngo 1.21\n')
        (root / 'cmd' / 'quivr').mkdir(parents=True)
        (root / 'cmd' / 'quivr' / 'main.go').write_text(MAIN)
        (root / 'extra').mkdir()
        (root / 'extra' / 'extra.go').write_text('package extra\n\nfunc Run() {}\n')
        # The plugin module replaces the SDK with its repository copy, as plugins/<id> do.
        (root / 'sdks' / 'go' / 'kit').mkdir(parents=True)
        (root / 'sdks' / 'go' / 'go.mod').write_text('module example.test/sdk\n\ngo 1.21\n')
        (root / 'sdks' / 'go' / 'kit' / 'kit.go').write_text('package kit\n\nfunc Serve() {}\n')
        (root / 'plugins' / 'feed').mkdir(parents=True)
        (root / 'plugins' / 'feed' / 'go.mod').write_text(
            'module example.test/feed\n\ngo 1.21\n\nrequire example.test/sdk v0.0.0\n\nreplace example.test/sdk => ../../sdks/go\n')
        (root / 'plugins' / 'feed' / 'main.go').write_text('package main\n\nimport "example.test/sdk/kit"\n\nfunc main() { kit.Serve() }\n')
        (root / 'Dockerfile').write_text(self.BUILD + connectors_stage)
        return root

    def test_plugin_builds_from_the_connectors_stage(self):
        root = self.repo('FROM golang AS connectors\nWORKDIR /src\nCOPY sdks/go ./sdks/go\nCOPY plugins ./plugins\n')
        self.assertIsNone(image_context.check(root, root / 'Dockerfile', GO))

    def test_missing_sdk_copy_fails_and_names_the_plugin(self):
        root = self.repo('FROM golang AS connectors\nWORKDIR /src\nCOPY plugins ./plugins\n')
        failure = image_context.check(root, root / 'Dockerfile', GO)
        self.assertIn('cannot build plugins/feed', failure)
        self.assertIn('sdks/go', failure)

    def test_missing_plugin_copy_fails(self):
        root = self.repo('FROM golang AS connectors\nWORKDIR /src\nCOPY sdks/go ./sdks/go\n')
        self.assertIn('does not copy plugins/feed', image_context.check(root, root / 'Dockerfile', GO))

    def test_missing_connectors_stage_fails(self):
        root = self.repo('')
        self.assertIn('no "connectors" stage', image_context.check(root, root / 'Dockerfile', GO))


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


class PythonImageContextTest(unittest.TestCase):
    """The stage that copies a Python script copies every repository module it imports."""
    def repo(self, copy_line):
        root = pathlib.Path(self.enterContext(tempfile.TemporaryDirectory()))
        (root / 'scripts').mkdir()
        (root / 'scripts' / 'prepare.py').write_text('import json\n\ndef run():\n    from helper import get\n')
        (root / 'scripts' / 'helper.py').write_text('import shared.paths\n')
        (root / 'scripts' / 'shared').mkdir()
        (root / 'scripts' / 'shared' / '__init__.py').write_text('')
        (root / 'Dockerfile').write_text(f'FROM python AS model\n{copy_line}\nRUN python scripts/prepare.py\nFROM tei\nCOPY scripts ./\n')
        return root

    def test_module_missing_from_the_stage_fails_and_names_it(self):
        root = self.repo('COPY scripts/prepare.py ./scripts/prepare.py')
        failure = image_context.check_python(root, root / 'Dockerfile', 'scripts/prepare.py')
        self.assertIn('does not copy scripts/helper.py, scripts/shared/__init__.py', failure)

    def test_directory_copy_covers_transitive_imports(self):
        root = self.repo('COPY scripts ./scripts')
        self.assertIsNone(image_context.check_python(root, root / 'Dockerfile', 'scripts/prepare.py'))


class PythonPluginImageContextTest(unittest.TestCase):
    def test_python_package_installs_only_when_copied_into_its_stage(self):
        # A tiny offline wheel exercises real pip installation, without an index or build backend.
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            wheel = 'fixture_plugin-1.0-py3-none-any.whl'
            with zipfile.ZipFile(root / wheel, 'w') as archive:
                archive.writestr('fixture_plugin.py', 'VALUE = 1\n')
                archive.writestr('fixture_plugin-1.0.dist-info/METADATA',
                                 'Metadata-Version: 2.1\nName: fixture-plugin\nVersion: 1.0\n')
                archive.writestr('fixture_plugin-1.0.dist-info/WHEEL',
                                 'Wheel-Version: 1.0\nGenerator: fixture\nRoot-Is-Purelib: true\nTag: py3-none-any\n')
                archive.writestr('fixture_plugin-1.0.dist-info/RECORD', '')
            dockerfile = root / 'Dockerfile'
            for copied in (False, True):
                with self.subTest(copied=copied):
                    dockerfile.write_text('FROM python AS plugins\n'
                                          + (f'COPY {wheel} /app/{wheel}\n' if copied else '')
                                          + f'RUN /opt/plugins/bin/pip install --no-index /app/{wheel}\n')
                    failure = image_context.check_python_plugins(root, dockerfile)
                    if copied:
                        self.assertIsNone(failure)
                    else:
                        self.assertIn('cannot install its Python packages', failure)
                        self.assertIn(wheel, failure)


if __name__ == '__main__':
    unittest.main()
