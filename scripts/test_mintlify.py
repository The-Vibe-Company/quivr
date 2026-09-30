"""Checks for the documentation site (docs-site/): generated pages, navigation, links and the configuration reference."""
import json
import unittest

import mintlify_site as site
from test_docs import Repo, tmpdir

INVENTORY = '''
dated = ["docs/adr/*.md"]
excluded = ["docs-site/*"]

[pages]
"README.md" = { audience = "functional", kind = "index" }
"docs/guide.md" = { audience = "contributor", kind = "guide" }
"docs/reference/cli.md" = { audience = "functional", kind = "generated-reference" }
"docs/reference/mcp.md" = { audience = "functional", kind = "generated-reference" }

[budgets]
"docs/guide.md" = 10
'''
# Stands in for the shared-schema bundler of the real contract.
BUNDLE = '''
def load():
    return {"openapi": "3.1.0", "info": {"title": "internal"}, "paths": {"/v0/saved-queries": {"get": {"operationId": "listSavedQueries"}}}}
'''
MANIFEST_SCHEMA = {
    'description': 'A plugin.', 'type': 'object', 'required': ['id', 'contributions'],
    'properties': {
        'id': {'type': 'string', 'maxLength': 64},
        'contributions': {'type': 'object', 'properties': {'normalizer': {'$ref': '#/$defs/Normalizer'}, 'enricher': False}},
    },
    '$defs': {'Normalizer': {'type': 'object', 'required': ['media_types'], 'properties': {
        'media_types': {'type': 'array', 'items': {'type': 'string'}, 'minItems': 1, 'maxItems': 32,
                        'description': 'Accepted media types'},
        'timeout_ms': {'type': 'integer', 'minimum': 1000, 'maximum': 300000, 'default': 30000}}}},
}
CONFIG_GO = '''package app

type Config struct {
	DatabaseURL string `json:"database_url"`
	// Listen is where the API serves.
	Listen string `json:"listen,omitempty"`
}
'''


class SiteRepo(Repo):
    """A checkout with a small site: two authored pages, the generated sources and a configuration struct."""

    def __init__(self, tmp):
        super().__init__(tmp, INVENTORY)
        self.write('docs/reference/cli.md', '# Command-line reference\n\n> Generated from `x`. Do not edit.\n\nSee [MCP](mcp.md) '
                   'and [the Quickstart](https://docs.quivr.thevibecompany.co/quickstart), built from [the table](../../scripts/tool.py).\n')
        self.write('docs/reference/mcp.md', '# MCP reference\n\nTools.\n')
        self.write('contracts/http/v0/openapi.yaml', 'openapi: 3.1.0\n')
        self.write('contracts/http/v0/bundle.py', BUNDLE)
        self.write('contracts/plugins/v0/plugin-manifest.schema.json', json.dumps(MANIFEST_SCHEMA))
        self.write('contracts/plugins/v0/health.schema.json', json.dumps(
            {'description': 'GET /v0/health returns 200 when ready.', 'type': 'object', 'required': ['status'],
             'properties': {'status': {'const': 'ok'}}}))
        self.write('contracts/plugins/v0/error.schema.json', json.dumps(
            {'description': 'Body of every error.', 'type': 'object', 'properties': {'code': {'type': 'string'}}}))
        self.write('internal/app/run.go', CONFIG_GO)
        self.write('docs-site/index.mdx', '---\ntitle: "Home"\n---\n\nStart with the [Quickstart](/quickstart).\n')
        self.write('docs-site/quickstart.mdx', '---\ntitle: "Quickstart"\n---\n\nBack [home](/).\n')
        self.write('docs-site/reference/configuration.mdx', '| `database_url` | … |\n| `listen` | … |\n')
        self.navigation(['index', 'quickstart', 'reference/configuration', 'reference/cli', 'reference/mcp',
                         'reference/plugin-manifest', 'reference/plugin-protocol'])

    def navigation(self, pages):
        self.write('docs-site/docs.json', json.dumps({'navigation': {'tabs': [
            {'tab': 'Docs', 'groups': [{'group': 'All', 'pages': pages}]}, {'tab': 'API', 'openapi': 'openapi.yaml'}]}}))

    def rules(self):
        return sorted((f.rule, f.path) for f in self.findings())


class Site(unittest.TestCase):
    def test_make_docs_fails_until_the_generated_pages_are_written_then_passes(self):
        with tmpdir() as tmp:
            repo = SiteRepo(tmp)
            self.assertEqual(repo.rules(), [('stale-docs-site', f'docs-site/{name}') for name in (
                'openapi.yaml', 'reference/cli.mdx', 'reference/mcp.mdx', 'reference/plugin-manifest.mdx',
                'reference/plugin-protocol.mdx')])
            site.write(tmp)
            self.assertEqual(repo.rules(), [])
            repo.write('docs/reference/mcp.md', '# MCP reference\n\nMore tools.\n')
            self.assertEqual(repo.rules(), [('stale-docs-site', 'docs-site/reference/mcp.mdx')])

    def test_navigation_links_and_configuration_keys_must_agree_with_the_pages(self):
        with tmpdir() as tmp:
            repo = SiteRepo(tmp)
            site.write(tmp)
            repo.write('docs-site/orphan.mdx', '---\ntitle: "Orphan"\n---\n\nSee [the guide](/guides/missing#step) '
                       'and\n\n```\n[not a link](/nowhere)\n```\n')
            repo.navigation(['index', 'quickstart', 'gone', 'reference/configuration', 'reference/cli', 'reference/mcp',
                             'reference/plugin-manifest', 'reference/plugin-protocol'])
            repo.write('docs-site/reference/configuration.mdx', '| `database_url` | … |\n')
            self.assertEqual(repo.rules(), [
                ('site-configuration', 'docs-site/reference/configuration.mdx'),
                ('site-link', 'docs-site/orphan.mdx'),
                ('site-navigation', 'docs-site/docs.json'),
                ('site-navigation', 'docs-site/orphan.mdx'),
            ])

    def test_generated_pages_link_to_the_site_and_describe_the_schemas(self):
        with tmpdir() as tmp:
            repo = SiteRepo(tmp)
            site.write(tmp)
            out = repo.root / site.SITE
            cli = (out / 'reference/cli.mdx').read_text()
            manifest = (out / 'reference/plugin-manifest.mdx').read_text()
            protocol = (out / 'reference/plugin-protocol.mdx').read_text()
            spec = (out / 'openapi.yaml').read_text()
        self.assertIn('See [MCP](/reference/mcp) and [the Quickstart](/quickstart), built from the table (`scripts/tool.py`).', cli)
        self.assertNotIn('Do not edit.', cli.split('*/}', 1)[1])  # the contributor notice stays in the repository
        self.assertIn('| `contributions.normalizer.media_types` | array of string | yes | Accepted media types. 1 to 32 items. |', manifest)
        self.assertIn('| `contributions.normalizer.timeout_ms` | integer | no | 1000 to 300000. Default `30000`. |', manifest)
        self.assertIn('| `contributions.enricher` | reserved | no |', manifest)
        self.assertIn('### `GET /v0/health`', protocol)
        self.assertIn('| `status` | `"ok"` | yes | — |', protocol)
        self.assertIn('title: Quivr HTTP API\n', spec)
        self.assertIn('summary: List saved queries\n', spec)
        self.assertIn('tags:\n      - Saved queries\n', spec)


class Conversion(unittest.TestCase):
    def test_title_moves_to_front_matter_and_mdx_syntax_is_escaped_outside_code(self):
        text = ('<!-- generated -->\n# The `quivr` guide\n\nUse {id} when a < b, see <https://example.com>.<br>\n'
                'Keep `{id} <b>` and\n\n```json\n{"a": "<b>"}\n```\n\n'
                '<details>\n<summary>Full schema</summary>\n\nx\n\n</details>\n\n## Setup\n\n## Setup\n'
                'export FOO=1\n')
        with tmpdir() as tmp:
            out = site.convert(site.Site(SiteRepo(tmp).root), 'docs/reference/cli.md', text)
        self.assertEqual(out, (
            '---\ntitle: "The quivr guide"\n---\n\n'
            'Use &#123;id&#125; when a &lt; b, see [https://example.com](https://example.com).<br />\n'
            'Keep `{id} <b>` and\n\n```json\n{"a": "<b>"}\n```\n\n'
            '<Accordion title="Full schema">\n\nx\n\n</Accordion>\n\n## Setup {#setup}\n\n## Setup {#setup-1}\n'
            '&#101;xport FOO=1\n'))


if __name__ == '__main__':
    unittest.main()
