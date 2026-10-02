"""Serve the fake Graph for `quivr plugin test plugins/m365-mail` (scripts/plugin_sdk_go.sh runs it as the fixture helper of plugins/m365-mail).

Starts tests/fakes/cmd/graph on an ephemeral port, seeds the application and the
mails that plugins/m365-mail/fixtures/mailbox.json expects (one with a PDF
attachment, which the runner's attachments check uploads to a grant), and
writes copies of the plugin's fixtures whose configuration endpoints point at
the fake into the output directory. Prints the fixture paths, then serves until
it is stopped. Nothing here is a real tenant, mailbox or secret.
"""
import base64, json, pathlib, signal, sys, threading, urllib.request

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from fake_api import Fake  # noqa: E402

ROOT = pathlib.Path(__file__).resolve().parents[1]
MAILBOX = 'monitoring-certify@example.org'


def main(out):
    out = pathlib.Path(out)
    out.mkdir(parents=True, exist_ok=True)
    stopped = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stopped.set())
    with Fake("graph") as graph:
        def post(path, body):
            req = urllib.request.Request(graph.url + path, data=json.dumps(body).encode(), method='POST', headers={'Content-Type': 'application/json'})
            urllib.request.urlopen(req, timeout=5).close()

        post('/_fake/apps', {'client_id': 'app-certify', 'secret': 'm365-certify-secret-not-real'})
        pdf = base64.b64encode('%PDF-1.7 pièce jointe'.encode()).decode()
        post('/_fake/messages', {'mailbox': MAILBOX, 'id': 'c1', 'received': '2026-09-28T10:00:00Z', 'subject': 'Dépêche c1', 'html': '<p>Première <b>dépêche</b></p>',
                                 'attachments': [{'id': 'a1', 'name': 'communique.pdf', 'content_type': 'application/pdf', 'data_b64': pdf},
                                                 {'id': 'a2', 'name': 'shared link', 'type': 'reference', 'size': 100}]})
        post('/_fake/messages', {'mailbox': MAILBOX, 'id': 'c2', 'received': '2026-09-28T10:05:00Z', 'subject': 'Dépêche c2', 'text': 'Texte brut'})
        paths = []
        for fixture in sorted((ROOT / 'plugins' / 'm365-mail' / 'fixtures').glob('*.json')):
            doc = json.loads(fixture.read_text())
            doc['configuration'] = {'login_endpoint': graph.url, 'graph_endpoint': graph.url + '/v1.0'}
            path = out / fixture.name
            path.write_text(json.dumps(doc, indent=2, ensure_ascii=False) + '\n')
            paths.append(str(path))
        print(' '.join(paths), flush=True)
        stopped.wait()


if __name__ == '__main__':
    main(sys.argv[1])
