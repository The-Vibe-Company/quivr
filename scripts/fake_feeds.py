"""Local test web site with RSS and Atom feeds for the browser demo (THE-732).

The demo's browser tests add feeds from this server, never from the internet.
Every page accepts an optional ?run=<id> that is carried into the feeds it
links and into their titles, so each test run gets its own sources.

- GET /site/       HTML page advertising one RSS feed
- GET /multi/      HTML page advertising two feeds (RSS and Atom)
- GET /plain/      HTML page without any feed
- GET /feeds/news.xml, /feeds/world.xml (RSS 2.0), /feeds/tech.atom (Atom)
- GET /feeds/ticker.xml  RSS whose every fetch adds one new item
- GET /_hits?path=/feeds/ticker.xml&run=<id>  {"hits": n} fetches of that feed

All content is synthetic.
"""
import email.utils, html, http.server, json, threading, time, urllib.parse


class Handler(http.server.BaseHTTPRequestHandler):
    hits = {}
    lock = threading.Lock()

    def log_message(self, *args):
        pass

    def send(self, status, content_type, body):
        data = body.encode()
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(data)))
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        url = urllib.parse.urlsplit(self.path)
        query = urllib.parse.parse_qs(url.query)
        run = query.get('run', [''])[0][:40]
        suffix = f' {run}' if run else ''
        tail = f'?run={urllib.parse.quote(run)}' if run else ''
        with self.lock:
            key = (url.path, run)
            self.hits[key] = self.hits.get(key, 0) + 1
            count = self.hits[key]
        if url.path == '/_hits':
            with self.lock:
                hits = self.hits.get((query.get('path', [''])[0], run), 0)
            return self.send(200, 'application/json', json.dumps({'hits': hits}))
        if url.path == '/site/':
            return self.send(200, 'text/html; charset=utf-8', page('Le Journal exemple' + suffix, [
                ('application/rss+xml', '/feeds/news.xml' + tail, 'Le Journal exemple — À la une' + suffix)]))
        if url.path == '/multi/':
            return self.send(200, 'text/html; charset=utf-8', page('La Revue exemple' + suffix, [
                ('application/rss+xml', '/feeds/world.xml' + tail, 'La Revue exemple — Monde' + suffix),
                ('application/atom+xml', '/feeds/tech.atom' + tail, 'La Revue exemple — Technologie' + suffix)]))
        if url.path == '/plain/':
            return self.send(200, 'text/html; charset=utf-8', page('Page sans flux', []))
        if url.path == '/feeds/news.xml':
            return self.send(200, 'application/rss+xml; charset=utf-8', rss('Le Journal exemple — À la une' + suffix, [
                ('news-1', 'Le phare de Kerlouan repeint en ocre', 'Les gardiens du phare ont choisi une teinte ocre inédite pour la façade.'),
                ('news-2', 'Un marché flottant ouvre sur le canal', 'Des maraîchers vendent leurs légumes depuis des barques amarrées.')], run))
        if url.path == '/feeds/world.xml':
            return self.send(200, 'application/rss+xml; charset=utf-8', rss('La Revue exemple — Monde' + suffix, [
                ('world-1', 'Les cartographes redessinent la lagune', 'Un relevé au sonar corrige les cartes anciennes de la lagune.')], run))
        if url.path == '/feeds/tech.atom':
            return self.send(200, 'application/atom+xml; charset=utf-8', atom('La Revue exemple — Technologie' + suffix, [
                ('tech-1', 'Un métier à tisser programmable par cartes perforées', 'Un atelier restaure un métier à tisser et ses cartes perforées.')], run))
        if url.path == '/feeds/ticker.xml':
            items = [(f'tick-{n}', f'Bulletin numéro {n}', f'Bulletin automatique numéro {n} du fil continu.') for n in range(max(1, count - 4), count + 1)]
            return self.send(200, 'application/rss+xml; charset=utf-8', rss('Fil continu exemple' + suffix, items, run))
        self.send(404, 'text/plain; charset=utf-8', 'not found')


def page(title, links):
    head = ''.join(f'<link rel="alternate" type="{t}" href="{html.escape(h)}" title="{html.escape(n)}">' for t, h, n in links)
    return f'<!doctype html><html lang="fr"><head><meta charset="utf-8"><title>{html.escape(title)}</title>{head}</head><body><h1>{html.escape(title)}</h1></body></html>'


def rss(title, items, run):
    now = email.utils.formatdate(time.time(), usegmt=True)
    body = ''.join(
        f'<item><guid isPermaLink="false">{key}-{html.escape(run)}</guid><title>{html.escape(t)}</title>'
        f'<description>{html.escape(d)}</description><pubDate>{now}</pubDate></item>' for key, t, d in items)
    return f'<?xml version="1.0" encoding="utf-8"?><rss version="2.0"><channel><title>{html.escape(title)}</title><link>http://example.invalid/</link><description>Flux de test</description>{body}</channel></rss>'


def atom(title, items, run):
    now = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
    body = ''.join(
        f'<entry><id>urn:quivr-test:{key}-{html.escape(run)}</id><title>{html.escape(t)}</title><updated>{now}</updated>'
        f'<summary>{html.escape(d)}</summary></entry>' for key, t, d in items)
    return f'<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>{html.escape(title)}</title><id>urn:quivr-test:{html.escape(run)}</id><updated>{now}</updated>{body}</feed>'


def start(host='127.0.0.1', port=0):
    """Serves in a daemon thread; returns (server, base URL)."""
    server = http.server.ThreadingHTTPServer((host, port), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, f'http://{host}:{server.server_address[1]}'


if __name__ == '__main__':
    server, base = start(port=int(__import__('os').environ.get('PORT', '0')))
    print('Fake feeds:', base, flush=True)
    threading.Event().wait()
