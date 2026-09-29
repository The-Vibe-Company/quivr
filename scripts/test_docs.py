"""Checks for the documentation inventory and link guard."""
import io
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

import docs as d

INVENTORY = '''
dated = ["history/**"]
excluded = ["vendor/**"]

[budgets]
"docs/guide.md" = 10

[pages]
"README.md" = { audience = "functional", kind = "index" }
"docs/guide.md" = { audience = "contributor", kind = "guide" }
'''


def tmpdir():
    # A background process on macOS can still be writing into .git at cleanup.
    return tempfile.TemporaryDirectory(ignore_cleanup_errors=True)


class Repo:
    """A throwaway git repository with a documentation inventory."""

    def __init__(self, tmp, inventory=INVENTORY):
        self.root = pathlib.Path(tmp)
        subprocess.run(['git', 'init', '-q', tmp], check=True)
        self.write('docs/inventory.toml', inventory)
        self.write('README.md', '# Project\n\nSee [the guide](docs/guide.md).\n')
        self.write('docs/guide.md', '# Guide\n\nRun `scripts/tool.py`.\n')
        self.write('scripts/tool.py', 'print(1)\n')

    def write(self, rel, text):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(text))

    def findings(self):
        return d.check(self.root)

    def rules(self):
        return [(f.rule, f.path, f.line) for f in self.findings()]


class Inventory(unittest.TestCase):
    def test_declared_repository_passes(self):
        with tmpdir() as tmp:
            self.assertEqual(Repo(tmp).findings(), [])

    def test_undeclared_page_fails_with_the_line_to_add(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/new.md', '# New\n')
            [finding] = repo.findings()
        self.assertEqual((finding.rule, finding.path, finding.line), ('unlisted-page', 'docs/new.md', 1))
        text = str(finding)
        self.assertIn('docs/new.md:1: [unlisted-page]', text)
        self.assertIn('"docs/new.md" = { audience = "', text)
        self.assertIn('docs/inventory.toml', text)

    def test_dated_and_excluded_pages_need_no_declaration(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('history/old.md', '[gone](nowhere.md) `scripts/removed.py`\n')
            repo.write('vendor/skill/SKILL.md', '[gone](nowhere.md)\n')
            self.assertEqual(repo.findings(), [])

    def test_ignored_files_are_not_pages(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('.gitignore', 'plans/\n')
            repo.write('plans/draft.md', '# Draft\n')
            self.assertEqual(repo.findings(), [])

    def test_declared_page_that_does_not_exist_fails(self):
        with tmpdir() as tmp:
            repo = Repo(tmp, INVENTORY + '"docs/gone.md" = { audience = "functional", kind = "guide" }\n')
            [finding] = repo.findings()
        self.assertEqual((finding.rule, finding.path), ('missing-page', 'docs/inventory.toml'))
        self.assertEqual(finding.line, INVENTORY.count('\n') + 1)
        self.assertIn('docs/gone.md', str(finding))
        self.assertIn('remove its line', str(finding))

    def test_unknown_audience_or_kind_fails(self):
        with tmpdir() as tmp:
            repo = Repo(tmp, INVENTORY.replace('kind = "guide"', 'kind = "tutorial"'))
            [finding] = repo.findings()
        guide_line = INVENTORY[:INVENTORY.index('"docs/guide.md" = {')].count('\n') + 1
        self.assertEqual((finding.rule, finding.line), ('invalid-entry', guide_line))  # its [pages] line, not [budgets]
        self.assertIn('tutorial', str(finding))
        self.assertIn('guide, concept, generated-reference, index', str(finding))

    def test_page_both_declared_and_dated_fails(self):
        with tmpdir() as tmp:
            repo = Repo(tmp, INVENTORY.replace('"history/**"', '"docs/**"'))
            [finding] = [f for f in repo.findings() if f.rule == 'invalid-entry']
        self.assertIn('docs/guide.md', str(finding))

    def test_malformed_inventory_fails_without_a_traceback(self):
        with tmpdir() as tmp:
            repo = Repo(tmp, '[pages\n')
            [finding] = repo.findings()
        self.assertEqual((finding.rule, finding.path), ('invalid-entry', 'docs/inventory.toml'))

    def test_inconsistent_inventories_fail_with_invalid_entry(self):
        pages = '[pages]\n"README.md" = { audience = "functional", kind = "index" }\n' \
                '"docs/guide.md" = { audience = "contributor", kind = "guide" }\n'
        cases = {
            'unknown key': 'owners = []\n' + pages,
            'dated not a list': 'dated = "history"\n' + pages,
            'extra field': pages.replace('kind = "guide" }', 'kind = "guide", owner = "x" }'),
            'pages not a table': 'pages = "x"\n',
            'not normalized': pages + '"./README.md" = { audience = "functional", kind = "index" }\n',
        }
        for name, inventory in cases.items():
            with self.subTest(name), tmpdir() as tmp:
                rules = {f.rule for f in Repo(tmp, inventory).findings()}
                self.assertIn('invalid-entry', rules)

    def test_missing_inventory_fails(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            (repo.root / 'docs/inventory.toml').unlink()
            [finding] = repo.findings()
        self.assertEqual((finding.rule, finding.path), ('invalid-entry', 'docs/inventory.toml'))


class Links(unittest.TestCase):
    def test_broken_relative_link_fails_with_file_and_line(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', '# Guide\n\nSee [old](renamed.md#usage).\n')
            [finding] = repo.findings()
        self.assertEqual((finding.rule, finding.path, finding.line), ('broken-link', 'docs/guide.md', 3))
        self.assertIn('docs/renamed.md', str(finding))
        self.assertIn('Fix:', str(finding))

    def test_resolving_links_pass(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', '''\
                # Guide
                [up](../README.md) [anchor](#guide) [dir](../scripts/) [root](/scripts/tool.py)
                [query](../README.md?plain=1#project) ![img](../scripts/tool.py "title")
                [ref]: ../scripts/tool.py
                <a href="../README.md">home</a> [spaced](<../README.md>)
                ''')
            self.assertEqual(repo.findings(), [])

    def test_reference_and_html_links_are_checked(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', '# Guide\n[ref]: missing-ref.md\n<img src="missing.png">\n')
            self.assertEqual(
                [(r, l) for r, _, l in repo.rules()], [('broken-link', 2), ('broken-link', 3)])

    def test_badge_multiline_and_parenthesised_links_are_checked(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', '''\
                [![badge](https://example.com/b.svg)](missing-license.md)
                See [the old
                guide](missing-wrapped.md) and [v2](missing_(v2).md).
                [ok](../scripts/tool.py)
                ''')
            self.assertEqual(
                [(f.line, f.message.split('"')[1]) for f in repo.findings()],
                [(1, 'missing-license.md'), (3, 'missing-wrapped.md'), (3, 'missing_(v2).md')])

    def test_footnotes_comments_and_escapes_are_not_links(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', '''\
                Text[^1].
                [Note]:
                This is prose, not a link target.
                [^1]: See the notes.
                <!-- [todo](not-yet.md) -->
                \\[literal\\](not-a-link.md)
                ~~~
                [fenced](missing.md)
                ~~~
                ''')
            self.assertEqual(repo.findings(), [])

    def test_external_links_are_not_fetched(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', '[x](https://invalid.example/missing.md) [m](mailto:a@example.com)\n')
            self.assertEqual(repo.findings(), [])

    def test_links_inside_code_are_ignored(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', '''\
                # Guide
                ```markdown
                [example](missing.md)
                `scripts/in-a-fence.py`
                ```
                Inline `[example](missing.md)` is code too.
                ''')
            self.assertEqual(repo.findings(), [])

    def test_reference_to_a_deleted_tracked_file_fails(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            subprocess.run(['git', '-C', tmp, 'add', '.'], check=True)
            (repo.root / 'scripts/tool.py').unlink()
            self.assertEqual(repo.rules(), [('missing-path', 'docs/guide.md', 3)])


class Paths(unittest.TestCase):
    def test_missing_repository_path_fails(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', '# Guide\n\nRun `python3 scripts/old_tool.py --flag`.\n')
            [finding] = repo.findings()
        self.assertEqual((finding.rule, finding.path, finding.line), ('missing-path', 'docs/guide.md', 3))
        self.assertIn('scripts/old_tool.py', str(finding))
        self.assertIn('Fix:', str(finding))

    def test_existing_paths_and_non_paths_pass(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', '''\
                # Guide
                `scripts/tool.py:12` `scripts/` `docs/guide.md#paths` `scripts/tool.py`,
                `origin/main` `feature/the-<n>-slug` `scripts/*.py` `scripts/{a,b}.py`
                `docs/…` `$HOME/scripts/x` `github.com/org/repo` `application/json`
                `plans/draft.md` `inventory.toml`
                ''')
            self.assertEqual(repo.findings(), [])

    def test_paths_beside_the_page_resolve(self):
        with tmpdir() as tmp:
            repo = Repo(tmp, INVENTORY + '"pkg/README.md" = { audience = "plugin-author", kind = "concept" }\n')
            repo.write('pkg/README.md', '`src/mod.py` and `src/gone.py`\n')
            repo.write('pkg/src/mod.py', '\n')
            self.assertEqual(repo.rules(), [('missing-path', 'pkg/README.md', 1)])

    def test_paths_inside_a_submodule_count_as_present(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            # A submodule that is not checked out, as in CI: only its gitlink is in the index.
            subprocess.run(['git', '-C', tmp, 'update-index', '--add', '--cacheinfo',
                            '160000,' + '1' * 40 + ',external'], check=True)
            repo.write('docs/guide.md', '[in sub](../external/deep/file.md) `external/deep/file.py`\n')
            self.assertEqual(repo.findings(), [])


GLOSSARY = """\
# Glossary

## Content

**Corpus**:
A collection of records.
_Avoid_: Index, database

**Record**:
One item in a corpus.
_Avoid_: Document
"""


class Budgets(unittest.TestCase):
    def test_page_at_its_budget_passes(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', 'line\n' * 10)
            self.assertEqual(repo.findings(), [])

    def test_page_over_its_budget_fails_at_the_first_line_over(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/guide.md', 'line\n' * 12)
            [finding] = repo.findings()
        self.assertEqual((finding.rule, finding.path, finding.line), ('over-budget', 'docs/guide.md', 11))
        text = str(finding)
        self.assertIn('12 lines', text)
        self.assertIn('budget of 10', text)
        self.assertIn('Fix: shorten', text)
        self.assertIn('signal', text)

    def test_guide_agents_and_glossary_without_a_budget_fail_with_the_line_to_add(self):
        inventory = INVENTORY.replace('"docs/guide.md" = 10\n', '') \
            + '"AGENTS.md" = { audience = "contributor", kind = "guide" }\n' \
            + '"CONTEXT.md" = { audience = "contributor", kind = "concept" }\n'
        with tmpdir() as tmp:
            repo = Repo(tmp, inventory)
            repo.write('AGENTS.md', '# Agents\n')
            repo.write('CONTEXT.md', GLOSSARY)
            findings = repo.findings()
        self.assertEqual(sorted((f.rule, f.path) for f in findings),
                         [('missing-budget', 'docs/inventory.toml')] * 3)
        self.assertTrue(any('`"CONTEXT.md" = 15`' in str(f) for f in findings))  # 11 lines plus headroom
        self.assertTrue(all('under [budgets]' in str(f) for f in findings))

    def test_concept_and_index_pages_need_no_budget(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('README.md', 'line\n' * 500)
            self.assertEqual(repo.findings(), [])

    def test_invalid_budgets_fail_with_invalid_entry(self):
        cases = {
            'zero': ('"docs/guide.md" = 10', '"docs/guide.md" = 0'),
            'not a number': ('"docs/guide.md" = 10', '"docs/guide.md" = "short"'),
            'undeclared page': ('"docs/guide.md" = 10', '"docs/guide.md" = 10\n"docs/other.md" = 5'),
        }
        for name, (old, new) in cases.items():
            with self.subTest(name), tmpdir() as tmp:
                findings = Repo(tmp, INVENTORY.replace(old, new)).findings()
                self.assertIn(('invalid-entry', 'docs/inventory.toml'), [(f.rule, f.path) for f in findings])

    def test_budget_of_a_declared_page_with_an_invalid_entry_is_not_reported_as_undeclared(self):
        with tmpdir() as tmp:
            inventory = INVENTORY.replace('kind = "guide" }', 'kind = "guide", owner = "x" }')
            findings = [str(f) for f in Repo(tmp, inventory).findings()]
        self.assertTrue(any('must set exactly audience and kind' in f for f in findings))
        self.assertFalse(any('not declared in [pages]' in f for f in findings))


class Glossary(unittest.TestCase):
    def repo(self, tmp, glossary):
        repo = Repo(tmp, INVENTORY.replace('[pages]', '"CONTEXT.md" = 40\n\n[pages]')
                    + '"CONTEXT.md" = { audience = "contributor", kind = "concept" }\n')
        repo.write('CONTEXT.md', glossary)
        return repo

    def test_terms_with_a_definition_and_an_avoid_line_pass(self):
        with tmpdir() as tmp:
            self.assertEqual(self.repo(tmp, GLOSSARY).findings(), [])

    def test_term_with_a_wrapped_or_inline_definition_passes(self):
        glossary = GLOSSARY.replace('A collection of records.', 'A collection of\n**Record** items.') \
            .replace('**Record**:\nOne item', '**Record**: One item')
        with tmpdir() as tmp:
            self.assertEqual(self.repo(tmp, glossary).findings(), [])

    def test_incomplete_terms_fail_at_the_term_line(self):
        cases = {
            'no definition': (GLOSSARY.replace('One item in a corpus.\n', ''), 'has no definition'),
            'no avoid line': (GLOSSARY.replace('_Avoid_: Document\n', ''), 'has no `_Avoid_:` line'),
            'empty avoid line': (GLOSSARY.replace('_Avoid_: Document', '_Avoid_:'), 'has no `_Avoid_:` line'),
            'avoid line after a blank line': (GLOSSARY.replace('\n_Avoid_: Document', '\n\n_Avoid_: Document'),
                                              'has no `_Avoid_:` line'),
        }
        for name, (glossary, message) in cases.items():
            with self.subTest(name), tmpdir() as tmp:
                [finding] = self.repo(tmp, glossary).findings()
                self.assertEqual((finding.rule, finding.path, finding.line), ('glossary-term', 'CONTEXT.md', 9))
                self.assertIn('"Record"', str(finding))
                self.assertIn(message, str(finding))
                self.assertIn('Fix: ', str(finding))

    def test_term_not_written_as_bold_then_colon_fails(self):
        for form in ('**Record:**', '**Record** (item):', '**Record**'):
            with self.subTest(form), tmpdir() as tmp:
                findings = self.repo(tmp, GLOSSARY.replace('**Record**:', form)).findings()
                self.assertIn(('glossary-term', 'CONTEXT.md', 9), [(f.rule, f.path, f.line) for f in findings])
                self.assertTrue(any('is not written as `**Record**:`' in str(f) for f in findings))

    def test_bold_text_in_fenced_code_is_not_a_term(self):
        with tmpdir() as tmp:
            glossary = GLOSSARY + '\n```\n**Example**:\n```\n'
            self.assertEqual(self.repo(tmp, glossary).findings(), [])


class Command(unittest.TestCase):
    def test_reports_and_fails(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            repo.write('docs/new.md', '[x](gone.md)\n')
            out = io.StringIO()
            status = d.main(['--root', tmp], stdout=out)
        self.assertEqual(status, 1)
        self.assertIn('docs/new.md:1: [unlisted-page]', out.getvalue())
        self.assertIn('1 documentation problem(s)', out.getvalue())

    def test_clean_repository_passes(self):
        with tmpdir() as tmp:
            Repo(tmp)
            out = io.StringIO()
            self.assertEqual(d.main(['--root', tmp], stdout=out), 0)


if __name__ == '__main__':
    unittest.main()
