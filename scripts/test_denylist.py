"""Checks for the hashed term denylist guard."""
import io
import pathlib
import subprocess
import tempfile
import unittest

import denylist as d

# Dummy terms used only by these tests.
TERMS = {d.digest('zorblax'), d.digest('acme widgets')}


class Tokens(unittest.TestCase):
    def test_splits_case_and_punctuation(self):
        self.assertEqual(
            d.tokens('ZorblaxPlugin plugin-zorblax zorblax.alerts XMLParser'),
            ['zorblax', 'plugin', 'plugin', 'zorblax', 'zorblax', 'alerts', 'xml', 'parser'],
        )


class Findings(unittest.TestCase):
    def matches(self, text):
        return [(line, term) for line, term in d.find_in_text(text, TERMS)]

    def test_single_term_in_any_form(self):
        for text in ('ZORBLAX', 'ZorBlax', 'Zorblax2026', 'plugin-zorblax/', 'the ZorblaxPlugin', 'id: zorblax.alerts'):
            self.assertEqual(self.matches(text), [(1, 'zorblax')], text)

    def test_two_word_term(self):
        self.assertEqual(self.matches('ok\nAcme  Widgets inc'), [(2, 'acme widgets')])
        self.assertEqual(self.matches('acme-widgets'), [(1, 'acme widgets')])

    def test_partial_words_and_clean_text_pass(self):
        self.assertEqual(self.matches('zorblaxes acme gadgets widgets'), [])

    def test_checksums_are_opaque(self):
        blob = 'Q2kLmNpRsT9Zorblax3xuVwQ2kLmNpRsTuVwQ2kLmNpRs='
        text = 'h1:9Zorblax3xQ2kLmNpRsTuVw= sha512-aZORBLAX1bQ2kLmNpRsTu "' + blob + '"'
        self.assertEqual(self.matches(text), [])

    def test_versioned_identifiers_and_paths_still_match(self):
        for text in ('internal/ZorblaxPlugin/v2', 'ZorblaxPluginV2Handler', 'github.com/org/ZorblaxDocs/tree/v2',
                     'docs/Zorblax2026Report.md', 'key=ZorblaxAlerts2',
                     'internal/adapters/ZorblaxPlugin/handlers/v2', 'ZorblaxPluginV2HandlerFactoryImplementation2x'):
            self.assertEqual(self.matches(text), [(1, 'zorblax')], text)
        self.assertEqual(self.matches('AcmeWidgetsV2Service'), [(1, 'acme widgets')])

    def test_digest_normalizes_the_term(self):
        self.assertEqual(d.digest('  Acme-Widgets '), d.digest('acme widgets'))


class Repository(unittest.TestCase):
    def test_scans_tracked_and_untracked_text_but_not_ignored_or_binary(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            subprocess.run(['git', 'init', '-q', tmp], check=True)
            (root / '.gitignore').write_text('ignored.txt\n')
            (root / 'tracked.md').write_text('clean\nzorblax\n')
            subprocess.run(['git', '-C', tmp, 'add', '.'], check=True)
            (root / 'untracked.md').write_text('Acme Widgets\n')
            (root / 'ignored.txt').write_text('zorblax\n')
            (root / 'image.bin').write_bytes(b'\x00zorblax')
            (root / 'zorblax-notes.md').write_text('clean\n')
            found = d.scan(root, TERMS)
        self.assertEqual(
            sorted((str(path), line, term) for path, line, term in found),
            [('tracked.md', 2, 'zorblax'), ('untracked.md', 1, 'acme widgets'), ('zorblax-notes.md', 0, 'zorblax')],
        )

    def test_denylist_file_ignores_comments(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / 'list'
            path.write_text('# comment\n\n' + d.digest('zorblax') + '\n')
            self.assertEqual(d.load(path), {d.digest('zorblax')})


class Command(unittest.TestCase):
    def test_stdin_mode_reports_and_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / 'list'
            path.write_text(d.digest('zorblax') + '\n')
            out = io.StringIO()
            code = d.main(['--denylist', str(path), '--stdin'], stdin=io.StringIO('a\nZorblax\n'), stdout=out)
            self.assertEqual(code, 1)
            self.assertIn('<stdin>:2', out.getvalue())
            clean = d.main(['--denylist', str(path), '--stdin'], stdin=io.StringIO('fine'), stdout=io.StringIO())
            self.assertEqual(clean, 0)


if __name__ == '__main__':
    unittest.main()
