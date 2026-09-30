"""Checks for the generated documentation site (docs-site/): its pages, navigation and freshness."""
import json
import unittest

import docs as d
import mintlify_nav as nav
import mintlify_site as site
from test_docs import Repo, tmpdir

INVENTORY = '''
dated = ["docs/adr/*.md"]
excluded = ["docs-site/*"]

[budgets]
"docs/guide.md" = 10

[pages]
"README.md" = { audience = "functional", kind = "index" }
"docs/guide.md" = { audience = "functional", kind = "guide", summary = "the `first` steps" }
"docs/reference/http-api.md" = { audience = "functional", kind = "generated-reference" }
"docs/start/functional.md" = { audience = "functional", kind = "start-page" }
"plugins/rss/README.md" = { audience = "plugin-author", kind = "concept" }
'''
# Stands in for the shared-schema bundler of the real contract.
BUNDLE = '''
def load():
    return {"openapi": "3.1.0", "paths": {"/v0/saved-queries": {"get": {"operationId": "listSavedQueries"}}}}
'''


class SiteRepo(Repo):
    def __init__(self, tmp):
        super().__init__(tmp, INVENTORY)
        self.write('docs/reference/http-api.md', '# HTTP API\n')
        self.write('plugins/rss/README.md', '# RSS plugin\n\n## Setup\n')
        self.write('docs/adr/0001-choice.md', '# Choice\n\nDate: 2026-01-01\nStatus: accepted\n')
        self.write('contracts/http/v0/openapi.yaml', 'openapi: 3.1.0\n')
        self.write('contracts/http/v0/bundle.py', BUNDLE)
        d.write_start_pages(self.root)

    def published(self):
        out = self.root / site.SITE
        return sorted(path.relative_to(out).as_posix() for path in out.rglob('*') if path.is_file())

    def convert(self, page, text):
        pages, *_ = d.load_inventory(self.root)
        return site.convert(site.Site(self.root, pages), page, text)


class GeneratedSite(unittest.TestCase):
    def test_make_docs_fails_until_the_site_is_regenerated_and_only_living_pages_are_published(self):
        with tmpdir() as tmp:
            repo = SiteRepo(tmp)
            self.assertEqual(repo.findings(), [], 'a checkout without docs-site/ is not checked')
            repo.write('docs-site/leftover.mdx', 'old\n')
            self.assertEqual([(f.rule, f.path) for f in repo.findings()],
                             [('stale-docs-site', 'docs-site/docs.json')])
            self.assertEqual(site.write(tmp)[1], [])
            self.assertEqual(repo.findings(), [])
            self.assertEqual(repo.published(), ['docs.json', 'guide.mdx', 'index.mdx', 'openapi.yaml',
                                                'plugins/rss/index.mdx', 'reference/http-api.mdx',
                                                'start/functional.mdx'])
            repo.write('docs/guide.md', '# Guide\n\nChanged.\n')
            self.assertEqual([(f.rule, f.path) for f in repo.findings()],
                             [('stale-docs-site', 'docs-site/guide.mdx')])
            config = json.loads((repo.root / site.SITE / 'docs.json').read_text())
            spec = (repo.root / site.SITE / 'openapi.yaml').read_text()
        docs_tab, api_tab = config['navigation']['tabs']
        self.assertEqual(api_tab, {'tab': 'API reference', 'openapi': 'openapi.yaml'})
        self.assertEqual(docs_tab['groups'], [
            {'group': 'Using Quivr', 'pages': ['index', 'start/functional', {'group': 'Guides', 'pages': ['guide']}]},
            {'group': 'Writing plugins', 'pages': [
                {'group': 'Understand how it works', 'pages': ['plugins/rss/index']}]},
        ])  # http-api is published but replaced by the API tab in the navigation
        self.assertIn('summary: List saved queries\n', spec)
        self.assertIn('tags:\n      - Saved queries\n', spec)

    def test_a_page_without_title_fails(self):
        with tmpdir() as tmp:
            repo = SiteRepo(tmp)
            repo.write('docs/guide.md', 'No title.\n')
            site.write(tmp)
            self.assertEqual([(f.rule, f.path) for f in repo.findings()],
                             [('missing-title', 'docs/guide.md'), ('stale-start-page', 'docs/start/functional.md')])

    def test_readme_routes_and_urls(self):
        self.assertEqual([nav.route(p) for p in ('README.md', 'AGENTS.md', 'docs/connectors/README.md')],
                         ['index', 'agents', 'connectors/index'])
        self.assertEqual([nav.url(p) for p in ('README.md', 'docs/connectors/README.md', 'docs/first-search.md')],
                         ['/', '/connectors', '/first-search'])


class Conversion(unittest.TestCase):
    def test_title_moves_to_front_matter_and_mdx_syntax_is_escaped_outside_code(self):
        text = ('<!-- generated -->\n# The `quivr` guide\n\nUse {id} when a < b, see <https://example.com>.<br>\n'
                'Keep `{id} <b>` and\n\n```json\n{"a": "<b>"}\n```\n\n'
                '<details>\n<summary>Full schema</summary>\n\nx\n\n</details>\n\n## Setup\n\n## Setup\n'
                'export FOO=1\n')
        with tmpdir() as tmp:
            out = SiteRepo(tmp).convert('docs/guide.md', text)
        self.assertEqual(out, (
            '---\ntitle: "The quivr guide"\n---\n\n'
            'Use &#123;id&#125; when a &lt; b, see [https://example.com](https://example.com).<br />\n'
            'Keep `{id} <b>` and\n\n```json\n{"a": "<b>"}\n```\n\n'
            '<Accordion title="Full schema">\n\nx\n\n</Accordion>\n\n## Setup {#setup}\n\n## Setup {#setup-1}\n'
            '&#101;xport FOO=1\n'))

    def test_links_point_to_site_pages_and_other_repository_files_become_code(self):
        text = ('# Guide\n\n[RSS](../plugins/rss/#setup), [plugin](../plugins/rss/README.md), '
                '[contract](../contracts/http/v0/openapi.yaml), [the tool](../scripts/tool.py), '
                '[ADR](adr/0001-choice.md), [scripts/tool.py](../scripts/tool.py), [web](https://example.com), '
                '[here](#setup), [`tool`](../scripts/tool.py)\n')
        with tmpdir() as tmp:
            out = SiteRepo(tmp).convert('docs/guide.md', text).split('---\n', 2)[2]
        self.assertEqual(out, (
            '\n[RSS](/plugins/rss#setup), [plugin](/plugins/rss), [contract](/openapi.yaml), '
            'the tool (`scripts/tool.py`), ADR (`docs/adr/0001-choice.md`), `scripts/tool.py`, '
            '[web](https://example.com), [here](#setup), `tool`\n'))


if __name__ == '__main__':
    unittest.main()
