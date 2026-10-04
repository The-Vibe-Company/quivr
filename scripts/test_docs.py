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
            'summary not text': pages.replace('kind = "guide" }', 'kind = "guide", summary = 3 }'),
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
        self.assertTrue(any('must set audience and kind' in f for f in findings))
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
FROZEN_INVENTORY = INVENTORY.replace('dated = ["history/**"]', 'dated = ["history/**", "docs/adr/*.md"]')
ACCEPTED = '# Use a queue\n\nStatus: accepted\n\nWe use a queue.\n'
PROPOSED = '# Use a cache\n\nStatus: proposed\n\nWe may use a cache.\n'
REPORT = '# Load test\n\nDate: 2026-09-01\nStatus: final\n\nIt held.\n'


def commit(root):
    for args in (['add', '-A'], ['-c', 'user.name=t', '-c', 'user.email=t@example.invalid',
                                 '-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base']):
        subprocess.run(['git', '-C', str(root), *args], check=True, capture_output=True)


class Frozen(unittest.TestCase):
    """Dated documents and accepted ADRs are compared with the base commit."""

    def repo(self, tmp):
        repo = Repo(tmp, FROZEN_INVENTORY)
        repo.write('docs/adr/0001-queue.md', ACCEPTED)
        repo.write('docs/adr/0002-cache.md', PROPOSED)
        repo.write('history/load.md', REPORT)
        commit(repo.root)
        return repo

    def frozen(self, repo):
        return [(f.rule, f.path, f.line) for f in d.check(repo.root, 'HEAD')]

    def test_unchanged_base_passes(self):
        with tmpdir() as tmp:
            self.assertEqual(self.frozen(self.repo(tmp)), [])

    def test_editing_an_accepted_adr_or_a_dated_document_fails_at_the_changed_line(self):
        for path, text in (('docs/adr/0001-queue.md', ACCEPTED), ('history/load.md', REPORT)):
            with self.subTest(path=path), tmpdir() as tmp:
                repo = self.repo(tmp)
                repo.write(path, text.replace('\n\n', '\n\nA quiet rewrite.\n', 1))
                [finding] = d.check(repo.root, 'HEAD')
                self.assertEqual((finding.rule, finding.path, finding.line), ('frozen-document', path, 3))
                self.assertRegex(finding.fix, rf'git checkout [0-9a-f]{{12}} -- {path}`')
                self.assertIn('supersedes it', finding.fix)

    def test_removing_or_moving_a_dated_document_fails(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            (repo.root / 'history/load.md').rename(repo.root / 'history/load-2026.md')
            self.assertEqual(self.frozen(repo), [('frozen-document', 'history/load.md', 1)])

    def test_new_documents_and_proposed_adrs_may_change(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            repo.write('docs/adr/0002-cache.md', PROPOSED.replace('may use', 'use').replace('proposed', 'accepted'))
            repo.write('docs/adr/0003-supersede-queue.md', '# Drop the queue\n\nDate: 2026-09-29\nStatus: accepted\n')
            repo.write('history/new.md', REPORT)
            self.assertEqual(self.frozen(repo), [])

    def test_a_path_that_is_no_longer_dated_may_move(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            repo.write('docs/inventory.toml', FROZEN_INVENTORY.replace('history/**', 'archive/*/*'))
            (repo.root / 'archive/reports').mkdir(parents=True)
            (repo.root / 'history/load.md').rename(repo.root / 'archive/reports/load.md')
            self.assertEqual(self.frozen(repo), [])

    def test_a_branch_behind_its_base_is_judged_on_its_own_edits(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            git = ['git', '-C', tmp]
            subprocess.run([*git, 'checkout', '-q', '-b', 'mainline'], check=True)
            repo.write('docs/adr/0002-cache.md', PROPOSED.replace('proposed', 'accepted'))
            repo.write('docs/adr/0003-later.md', '# Later\n\nDate: 2026-09-29\nStatus: accepted\n')
            commit(repo.root)
            subprocess.run([*git, 'checkout', '-q', '-'], check=True, capture_output=True)
            self.assertEqual(self.frozen_against(repo, 'mainline'), [])
            repo.write('docs/adr/0001-queue.md', ACCEPTED + 'Edited.\n')
            self.assertEqual(self.frozen_against(repo, 'mainline'), [('frozen-document', 'docs/adr/0001-queue.md', 6)])

    def frozen_against(self, repo, base):
        return [(f.rule, f.path, f.line) for f in d.check(repo.root, base)]

    def test_new_dated_document_without_date_or_status_fails(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            repo.write('history/undated.md', '# Undated\n\nStatus: final\n')
            repo.write('docs/adr/0003-bare.md', '# Bare\n\nWe decided.\n')
            findings = d.check(repo.root, 'HEAD')
        self.assertEqual([(f.rule, f.path) for f in findings],
                         [('dated-header', 'docs/adr/0003-bare.md'), ('dated-header', 'history/undated.md')])
        self.assertIn('Date: YYYY-MM-DD and Status: <status>', findings[0].fix)
        self.assertIn('no Date: YYYY-MM-DD line', findings[1].message)

    def test_unresolvable_base_fails_with_the_fetch_command(self):
        with tmpdir() as tmp:
            [finding] = d.check(self.repo(tmp).root, 'origin/main')
        self.assertEqual(finding.rule, 'frozen-document')
        self.assertIn('git fetch --no-tags origin', finding.fix)


START_PAGES = INVENTORY + '''"docs/start/contributor.md" = { audience = "contributor", kind = "start-page" }
"docs/start/functional.md" = { audience = "functional", kind = "start-page" }
'''


class StartPages(unittest.TestCase):
    def repo(self, tmp, inventory=START_PAGES):
        repo = Repo(tmp, inventory)
        repo.write('history/old.md', '# Old design\n')
        d.write_start_pages(repo.root)
        return repo

    def page(self, repo, audience):
        return (repo.root / f'docs/start/{audience}.md').read_text()

    def test_generated_start_pages_pass_and_list_each_page_for_its_reader(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            self.assertEqual(repo.findings(), [])
            contributor, functional = self.page(repo, 'contributor'), self.page(repo, 'functional')
        self.assertIn('- [Guide](../guide.md)', contributor)
        self.assertIn('- [Project](../../README.md)', functional)
        self.assertNotIn('guide.md', functional)
        self.assertIn('- [Using Quivr](functional.md)', contributor)  # other readers
        self.assertNotIn('history/old.md', contributor + functional)  # dated documents are never listed

    def test_declaring_a_page_without_regenerating_fails_with_the_command(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            repo.write('docs/new.md', '# New page\n')
            repo.write('docs/inventory.toml', START_PAGES + '"docs/new.md" = { audience = "contributor", kind = "concept" }\n')
            [finding] = repo.findings()
        self.assertEqual((finding.rule, finding.path), ('stale-start-page', 'docs/start/contributor.md'))
        self.assertIn('make start-pages', str(finding))

    def test_regenerating_lists_a_new_page_of_a_kind_not_used_before_with_its_summary(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            repo.write('docs/reference/api.md', '# API reference\n')
            repo.write('docs/inventory.toml', START_PAGES + '"docs/reference/api.md" = '
                       '{ audience = "functional", kind = "generated-reference", summary = "every endpoint" }\n')
            d.write_start_pages(repo.root)
            self.assertEqual(repo.findings(), [])
            functional = self.page(repo, 'functional')
        self.assertIn('## Reference\n\n- [API reference](../reference/api.md): every endpoint\n', functional)

    def test_retitling_a_page_or_editing_a_start_page_by_hand_is_stale(self):
        for rel, text in (('docs/guide.md', '# Renamed guide\n'), ('docs/start/functional.md', '# Mine\n')):
            with self.subTest(rel), tmpdir() as tmp:
                repo = self.repo(tmp)
                repo.write(rel, text)
                self.assertEqual([f.rule for f in repo.findings()], ['stale-start-page'])

    def test_missing_start_page_names_the_command(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            (repo.root / 'docs/start/functional.md').unlink()
            [finding] = [f for f in repo.findings() if f.rule == 'missing-page']  # the other page's link breaks too
        self.assertIn('make start-pages', str(finding))

    def test_second_start_page_for_an_audience_fails(self):
        inventory = START_PAGES + '"docs/start/other.md" = { audience = "contributor", kind = "start-page" }\n'
        with tmpdir() as tmp:
            repo = self.repo(tmp, inventory)
            [finding] = [f for f in repo.findings() if f.rule == 'invalid-entry']
        self.assertIn('second start page', str(finding))

    def test_malformed_start_page_entry_is_reported_not_raised(self):
        for audience in ('["functional"]', '"readers"'):
            inventory = START_PAGES + f'"docs/start/other.md" = {{ audience = {audience}, kind = "start-page" }}\n'
            with self.subTest(audience), tmpdir() as tmp:
                repo = self.repo(tmp, inventory)
                self.assertIn('invalid-entry', [f.rule for f in repo.findings()])

    def test_title_keeps_a_hash_that_is_part_of_it(self):
        with tmpdir() as tmp:
            repo = self.repo(tmp)
            repo.write('docs/guide.md', '# Plugins in C# ##\n')
            d.write_start_pages(repo.root)
            self.assertIn('- [Plugins in C#](../guide.md)', self.page(repo, 'contributor'))

    def test_command_writes_the_start_pages(self):
        with tmpdir() as tmp:
            repo = Repo(tmp, START_PAGES)
            commit(repo.root)
            out = io.StringIO()
            status = d.main(['--root', tmp, '--base', 'HEAD', '--write-start-pages'], stdout=out)
        self.assertEqual(status, 0)
        self.assertIn('wrote docs/start/functional.md', out.getvalue())


class Command(unittest.TestCase):
    def test_reports_and_fails(self):
        with tmpdir() as tmp:
            repo = Repo(tmp)
            commit(repo.root)
            repo.write('docs/new.md', '[x](gone.md)\n')
            out = io.StringIO()
            status = d.main(['--root', tmp, '--base', 'HEAD'], stdout=out)
        self.assertEqual(status, 1)
        self.assertIn('docs/new.md:1: [unlisted-page]', out.getvalue())
        self.assertIn('1 documentation problem(s)', out.getvalue())

    def test_clean_repository_passes(self):
        with tmpdir() as tmp:
            commit(Repo(tmp).root)
            out = io.StringIO()
            self.assertEqual(d.main(['--root', tmp, '--base', 'HEAD'], stdout=out), 0)


if __name__ == '__main__':
    unittest.main()
