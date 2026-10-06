"""Which external normalizer the local harness pins (THE-686). Runs without Docker."""
import shutil, unittest, uuid
from unittest import mock

import local
import normalizer_plugin as np


class Selection(unittest.TestCase):
    def setUp(self):
        self.name = 'quivr-test-' + uuid.uuid4().hex[:10]
        self.stack = local.Stack(self.name)

    def tearDown(self):
        shutil.rmtree(local.ROOT / '.scratch' / self.name, ignore_errors=True)

    def test_no_selection_pins_nothing(self):
        self.assertIsNone(np.pin(self.stack))

    def test_none_disables_the_pin(self):
        np.select(self.stack, 'none')
        self.assertIsNone(np.pin(self.stack))

    def test_first_party_normalizers_pin_their_required_routes(self):
        for name, types in [
            ('pdf-text', ['application/pdf']),
            ('newsml-g2', ['application/vnd.iptc.g2.newsitem+xml',
                           'application/vnd.iptc.g2.newsmessage+xml']),
        ]:
            with self.subTest(name=name):
                np.select(self.stack, name)
                pin = np.pin(self.stack)
                self.assertEqual(pin['manifest'], str(local.ROOT / 'plugins' / name / 'quivr-plugin.yaml'))
                self.assertEqual(pin['routes'], [{'media_type': value, 'mode': 'required'} for value in types])
                self.assertEqual(pin['configuration'], {})
                self.assertTrue(pin['endpoint'].startswith('http://127.0.0.1:'))

    def test_template_pins_markdown_once_scaffolded(self):
        np.select(self.stack, 'template')
        self.assertIsNone(np.pin(self.stack))
        np.directory(self.stack).mkdir(parents=True)
        np.manifest(self.stack).write_text('id: markdown-sections\n')
        pin = np.pin(self.stack)
        self.assertEqual(pin['routes'], [{'media_type': 'text/markdown', 'mode': 'required'}])
        self.assertEqual(pin['configuration'], {'max_sections': 32})

    def test_selection_survives_reload(self):
        np.select(self.stack, 'pdf-text')
        self.assertEqual(np.selected(local.Stack(self.name)), 'pdf-text')

    def test_unknown_selection_is_refused(self):
        with self.assertRaisesRegex(ValueError, 'pdf-text.*template.*none'):
            np.select(self.stack, 'pdf')

    def test_a_plugin_directory_is_pinned_at_the_port_its_author_runs_it_on(self):
        plugin = local.ROOT / '.scratch' / self.name / 'my-plugin'
        plugin.mkdir(parents=True)
        (plugin / 'quivr-plugin.yaml').write_text('id: my-plugin\n')
        env = {'QUIVR_NORMALIZER_PORT': '9911', 'QUIVR_NORMALIZER_CONFIG': '{"limit": 3}'}
        with mock.patch.dict(np.os.environ, env), mock.patch.object(np, 'media_types', return_value=['text/csv', 'text/tab-separated-values']):
            np.select(self.stack, str(plugin))
            pin = np.pin(self.stack)
        self.assertEqual(pin, {'manifest': str(plugin / 'quivr-plugin.yaml'), 'endpoint': 'http://127.0.0.1:9911', 'configuration': {'limit': 3},
                               'routes': [{'media_type': 'text/csv', 'mode': 'required'}, {'media_type': 'text/tab-separated-values', 'mode': 'required'}]})
        with mock.patch.object(np, 'stop') as stop, mock.patch.object(np.subprocess, 'Popen') as popen:
            np.start(self.stack)
        stop.assert_called_once()
        popen.assert_not_called()  # the author runs it: quivr plugin dev --port 9911

    def test_a_directory_without_manifest_is_refused(self):
        with self.assertRaisesRegex(ValueError, 'quivr-plugin.yaml'):
            np.select(self.stack, str(local.ROOT / 'scripts'))

    def test_make_dev_defaults_to_pdf_text(self):
        with mock.patch.dict(np.os.environ, {}, clear=True):
            self.assertEqual(np.from_environment(), 'pdf-text')
        with mock.patch.dict(np.os.environ, {'QUIVR_NORMALIZER': 'none'}):
            self.assertEqual(np.from_environment(), 'none')


if __name__ == '__main__':
    unittest.main()
