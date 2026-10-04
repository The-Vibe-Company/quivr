"""Runnable guide blocks: which pages are replayed, what passes and what the failure report says."""
import contextlib
import io
import json
import pathlib
import tempfile
import textwrap
import unittest

import guides

FENCE = '```'


def page(*parts):
    return textwrap.dedent('\n'.join(parts)).replace("'''", FENCE)


def command(code, output=None, retry=False, kind='json'):
    text = f"'''sh runnable{' retry' if retry else ''}\n{code}\n'''\n"
    if output is not None:
        text += f"\nSome prose between the command and its output.\n\n'''{kind} output\n{output}\n'''\n"
    return text.replace("'''", FENCE)


class Repository:
    """A fixture checkout: an inventory and the pages it declares."""

    def __init__(self, test, pages, undeclared=()):
        directory = tempfile.TemporaryDirectory()
        test.addCleanup(directory.cleanup)
        self.root = pathlib.Path(directory.name)
        declared = '\n'.join(f'"{path}" = {{ audience = "functional", kind = "guide" }}' for path in pages)
        (self.root / 'docs').mkdir()
        (self.root / guides.INVENTORY).write_text(f'[pages]\n{declared}\n')
        for path, text in {**pages, **dict(undeclared)}.items():
            (self.root / path).parent.mkdir(parents=True, exist_ok=True)
            (self.root / path).write_text(text)

    def replay(self, guide, env=None, **kwargs):
        return guides.replay(guides.pages(self.root)[guide], {'PATH': '/usr/bin:/bin', **(env or {})}, **kwargs)


class FakeStack:
    def __init__(self, directory):
        self.state, self.directory, self.saved = {'api_port': 1}, pathlib.Path(directory), 0

    def save(self):
        self.saved += 1


class ReplayTest(unittest.TestCase):
    def test_a_passing_guide_carries_kept_values_and_asserts_only_what_it_shows(self):
        repo = Repository(self, {'docs/first.md': page(
            '# First',
            command('''printf '{"corpus_id": "c-%s", "created_at": "2026", "items": [1, 2, 3], "url": "%s"}' "$RANDOM" "$QUIVR_API_URL"''',
                    '{"corpus_id": "{{CORPUS_ID}}", "items": [1, "..."], "url": "http://quivr.test"}'),
            "'''sh\nexport CORPUS_ID=<the corpus_id above>; exit 1\n'''",
            command('printf \'{"corpus": "%s", "same": "%s"}\' "$CORPUS_ID" "$CORPUS_ID"',
                    '{"corpus": "{{CORPUS_ID}}", "same": "{{CORPUS_ID}}"}'),
            command('printf "done\\n"', 'done', kind='text'),
        )})
        results = repo.replay('docs/first.md', {'QUIVR_API_URL': 'http://quivr.test'})
        self.assertEqual([(r['block'], r['status']) for r in results], [(1, 'passed'), (2, 'passed'), (3, 'passed')])

    def test_a_broken_block_names_the_guide_the_block_and_the_difference_and_stops_the_guide(self):
        guide = page(
            '# Broken',
            command('printf \'{"record_id": "r-1"}\'', '{"record_id": "{{RECORD_ID}}"}'),
            command('printf \'{"items": [{"record_id": "r-1", "excerpt": {"text": "Monday"}}]}\'',
                    '{"items": [{"record_id": "{{RECORD_ID}}", "excerpt": {"text": "Tuesday"}}]}'),
            command('printf never-run'),
        )
        repo = Repository(self, {'docs/broken.md': guide})
        results = repo.replay('docs/broken.md')
        self.assertEqual([r['status'] for r in results], ['passed', 'failed'])
        second = [n for n, text in enumerate(guide.splitlines(), 1) if text == '```sh runnable'][1]
        self.assertEqual(guides.failures(results), [
            f'docs/broken.md:{second}: block 2: $.items[0].excerpt.text: expected "Tuesday", got "Monday"; '
            'output "{\\"items\\": [{\\"record_id\\": \\"r-1\\", \\"excerpt\\": {\\"text\\": \\"Monday\\"}}]}"'])

    def test_each_kind_of_difference_is_reported(self):
        cases = {
            'a missing key': ('printf \'{"id": "a"}\'', '{"id": "{{ID}}", "again": "{{ID}}"}',
                                          '$.again: missing'),
            'a different kept value': ('printf \'{"id": "a", "again": "b"}\'', '{"id": "{{ID}}", "again": "{{ID}}"}',
                                       '$.again: expected ID = "a", got "b"'),
            'an array of another length': ('printf \'{"items": [1, 2]}\'', '{"items": [1]}',
                                           '$.items: expected 1 items, got 2'),
            'a type change': ('printf \'{"rank": true}\'', '{"rank": 1}', '$.rank: expected 1, got true'),
            'an object kept as a value': ('printf \'{"id": {}}\'', '{"id": "{{ID}}"}', '$.id: ID must be a string or a number, got {}'),
            'output that is not JSON': ('printf "<html>"', '{}', 'output is not JSON: "<html>"'),
            'a failing command': ('echo "curl: (7) Failed to connect" >&2; exit 7', '{}',
                                  'exit status 7: "curl: (7) Failed to connect"'),
        }
        for name, (code, output, reason) in cases.items():
            with self.subTest(name):
                repo = Repository(self, {'docs/case.md': command(code, output)})
                [error] = guides.failures(repo.replay('docs/case.md'))
                self.assertIn(': block 1: ' + reason, error)

    def test_a_retry_block_is_rerun_until_its_output_matches(self):
        # The state file lives in the page's scratch folder: pending twice, then resolved.
        code = 'n=$(cat count 2>/dev/null || echo 0); echo $((n+1)) > count; ' \
               '[ "$n" -ge 2 ] && printf \'{"state": "resolved"}\' || printf \'{"state": "pending"}\''
        repo = Repository(self, {'docs/wait.md': command(code, '{"state": "resolved"}', retry=True)})
        [result] = repo.replay('docs/wait.md', pause=0)
        self.assertEqual((result['status'], result['attempts']), ('passed', 3))

    def test_a_retry_block_gives_up_with_its_last_difference(self):
        repo = Repository(self, {'docs/wait.md': command('printf \'{"state": "pending"}\'', '{"state": "resolved"}', retry=True)})
        [error] = guides.failures(repo.replay('docs/wait.md', retry_seconds=0.2, pause=0.05))
        self.assertRegex(error, r'block 1: \$\.state: expected "resolved", got "pending".* \(after \d+ attempts\)$')


class PagesTest(unittest.TestCase):
    def test_only_declared_pages_with_runnable_blocks_are_replayed(self):
        repo = Repository(self, {
            'docs/runnable.md': command('true'),
            'docs/plain.md': "'''sh\nmake dev\n'''\n".replace("'''", FENCE),
            'docs/nested.md': page("````markdown", command('true'), "````"),
        }, undeclared={'docs/draft.md': command('true')})
        self.assertEqual(list(guides.pages(repo.root)), ['docs/runnable.md'])

    def test_an_output_block_that_follows_no_command_is_refused(self):
        for text in (page("'''json output\n{}\n'''"), page(command('true'), "'''sh\nls\n'''", "'''json output\n{}\n'''")):
            with self.subTest(text=text):
                repo = Repository(self, {'docs/orphan.md': text})
                with self.assertRaisesRegex(guides.GuideError, r'^docs/orphan\.md:\d+: an output block must directly follow'):
                    guides.pages(repo.root)


    def test_a_kept_value_readers_are_never_told_to_export_is_refused(self):
        text = command('printf \'{"id": "a"}\'', '{"id": "{{RECEIPT_ID}}"}') + command('echo "$RECEIPT_ID"')
        repo = Repository(self, {'docs/unexported.md': text})
        with self.assertRaisesRegex(guides.GuideError, r'^docs/unexported\.md:\d+: block 2: uses \$RECEIPT_ID, which readers are never told to set'):
            guides.pages(repo.root)

    def test_a_kept_value_may_not_replace_the_stack_address_or_key(self):
        repo = Repository(self, {'docs/reserved.md': command('printf \'{"url": "x"}\'', '{"url": "{{QUIVR_API_URL}}"}')})
        with self.assertRaisesRegex(guides.GuideError, r'\{\{QUIVR_API_URL\}\} would replace'):
            guides.pages(repo.root)

    def test_the_repository_guides_are_readable(self):
        # Authoring errors surface in make test on any platform, before the Linux-only replay.
        self.assertIn('docs-site/quickstart.mdx', guides.pages())

    def test_a_site_page_marks_its_blocks_with_mdx_comments(self):
        mdx = page("{/* runnable retry */}", "'''bash", 'printf \'{"id": "a"}\'', "'''", '',
                   "{/* output */}", "'''json", '{"id": "{{ID}}"}', "'''", '', "'''bash", 'ls', "'''")
        repo = Repository(self, {}, undeclared={'docs-site/guide.mdx': mdx})
        (found,) = guides.pages(repo.root).values()
        self.assertEqual([(b.line, b.retry, b.expected) for b in found], [(2, True, '{"id": "{{ID}}"}\n')])


class VerifyTest(unittest.TestCase):
    def test_each_page_gets_its_own_organization_and_a_stable_key(self):
        repo = Repository(self, {'docs/a.md': command('true'), 'docs/b.md': command('true'), 'docs/none.md': '# No blocks\n'})
        stack = FakeStack(repo.root)
        first = guides.keys(stack, repo.root)
        self.assertEqual(guides.keys(stack, repo.root), first)
        self.assertEqual(len({scope['organization'] for scope in first.values()}), 2)
        self.assertTrue(all(len(token) >= 32 for token in first))
        receivers = guides.destinations(stack, repo.root)
        self.assertEqual(sorted(d['organization'] for d in receivers.values()),
                         sorted(scope['organization'] for scope in first.values()))

    def test_an_unreadable_page_fails_the_replay_step_not_the_stack_start(self):
        repo = Repository(self, {'docs/orphan.md': "```json output\n{}\n```\n"})
        stack = FakeStack(repo.root)
        self.assertEqual(guides.keys(stack, repo.root), {})
        with self.assertRaisesRegex(guides.GuideError, 'an output block must directly follow'):
            guides.verify(stack, repo.root)

    def test_verify_fails_when_no_page_has_a_runnable_block(self):
        repo = Repository(self, {'docs/none.md': '# No blocks\n'})
        with self.assertRaisesRegex(AssertionError, 'no runnable block'):
            guides.verify(FakeStack(repo.root), repo.root)

    def test_verify_replays_every_page_against_the_stack_and_writes_the_report(self):
        repo = Repository(self, {
            'docs/a.md': command('[ -n "$QUIVR_API_KEY" ] && [ -n "$QUIVR_DESTINATION" ] && printf \'{"url": "%s"}\' "$QUIVR_API_URL"',
                                 '{"url": "http://127.0.0.1:1"}'),
            'docs/b.md': command('printf \'{"state": "%s"}\' pending', '{"state": "resolved"}'),
        })
        stack = FakeStack(repo.root)
        guides.keys(stack, repo.root)
        with self.assertRaises(AssertionError) as failed:
            guides.verify(stack, repo.root)
        self.assertRegex(str(failed.exception), r'docs/b\.md:1: block 1: \$\.state: expected "resolved", got "pending"')
        report = json.loads((repo.root / 'guides.json').read_text())
        self.assertEqual([(r['guide'], r['status']) for r in report], [('docs/a.md', 'passed'), ('docs/b.md', 'failed')])

    def test_the_command_line_lists_the_runnable_blocks(self):
        repo = Repository(self, {'docs/a.md': command('true', retry=True)})
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(guides.main(['--root', str(repo.root), '--list']), 0)
        self.assertEqual(out.getvalue(), 'docs/a.md:1: block 1 (retry)\n')


if __name__ == '__main__':
    unittest.main()
