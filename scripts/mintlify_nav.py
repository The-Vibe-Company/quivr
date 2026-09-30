"""Routes and navigation of the documentation site, derived from docs/inventory.toml.

scripts/mintlify_site.py writes the navigation into docs-site/docs.json: one
group per audience, the home page and the audience's start page first, then its
pages grouped by kind in the start pages' order and sorted by route, and an API
reference tab built from the OpenAPI contract.

Each living page is published at a route derived from its path: `docs/` is
dropped, `README` becomes `index` (the repository README is the home page), and
the result is lower-cased, so `plugins/rss/README.md` is `/plugins/rss` and
`docs/connectors/x.md` is `/connectors/x`.
"""
SPEC = 'openapi.yaml'  # the contract's path in the site
# Living pages that are published but not listed: the site's API tab replaces them.
UNLISTED = ('docs/reference/http-api.md',)
DOCS_TAB = 'Documentation'
API_TAB = 'API reference'


def route(page):
    """The site route of a living page, without a leading slash."""
    path = page[:-len('.md')] if page.lower().endswith('.md') else page
    if path.startswith('docs/'):
        path = path[len('docs/'):]
    head, _, name = path.rpartition('/')
    if name.lower() == 'readme':
        path = f'{head}/index' if head else 'index'
    return path.lower()


def url(page):
    """The root-relative URL of a living page on the site."""
    path = route(page)
    if path == 'index':
        return '/'
    return '/' + (path[:-len('/index')] if path.endswith('/index') else path)


def navigation(pages, readers, sections, kinds):
    """The `navigation` value of docs-site/docs.json for the living `pages`.

    `readers` maps an audience to (group title, intro), `sections` a kind to its
    group title, and `kinds` lists every kind: the ones in `sections` come first,
    in that order, as on the start pages.
    """
    order = [kind for kind in sections if kind in kinds] + [kind for kind in kinds if kind not in sections]
    groups = []
    for audience, (title, _) in readers.items():
        listed = sorted((page for page in pages if pages[page]['audience'] == audience and page not in UNLISTED),
                        key=route)
        # The home page (the repository README) opens its group, then the audience's start page.
        entries = [route(page) for page in listed if route(page) == 'index']
        entries += [route(page) for page in listed if pages[page]['kind'] == 'start-page']
        for kind in order:
            if kind == 'start-page':
                continue
            routes = [route(page) for page in listed if pages[page]['kind'] == kind and route(page) != 'index']
            if routes:
                entries.append({'group': sections.get(kind, kind.replace('-', ' ').capitalize()), 'pages': routes})
        if entries:
            groups.append({'group': title, 'pages': entries})
    return {'tabs': [{'tab': DOCS_TAB, 'groups': groups}, {'tab': API_TAB, 'openapi': SPEC}]}
