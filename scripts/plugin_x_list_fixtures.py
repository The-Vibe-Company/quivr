"""Serve the fake X API for `quivr plugin test plugins/x-list` (scripts/plugin_sdk_go.sh runs it as the fixture helper of plugins/x-list).

Starts the shared Go X fake on a bound ephemeral port, seeds the posts of list 77 that
plugins/x-list/fixtures/list.json expects, and writes copies of the plugin's
fixtures whose configuration.api_endpoint points at the fake into the output
directory. Prints the fixture paths, then serves until it is stopped.
"""
import json, pathlib, signal, sys, threading, urllib.request

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from fake_api import Fake  # noqa: E402

ROOT = pathlib.Path(__file__).resolve().parents[1]


def main(out):
    out = pathlib.Path(out)
    out.mkdir(parents=True, exist_ok=True)
    stopped = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stopped.set())
    with Fake("x") as fake:
        endpoint = fake.url
        posts = [{'id': f'180000000000000000{i}', 'text': f'Post {i}', 'created_at': f'2025-12-31T0{i}:00:00Z'} for i in range(1, 8)]
        req = urllib.request.Request(endpoint + '/_control/lists/77', data=json.dumps({'posts': posts}).encode(), method='POST', headers={'Content-Type': 'application/json'})
        urllib.request.urlopen(req, timeout=5).close()
        paths = []
        for fixture in sorted((ROOT / 'plugins' / 'x-list' / 'fixtures').glob('*.json')):
            doc = json.loads(fixture.read_text())
            doc['configuration'] = {'api_endpoint': endpoint}
            path = out / fixture.name
            path.write_text(json.dumps(doc, indent=2) + '\n')
            paths.append(str(path))
        print(' '.join(paths), flush=True)
        stopped.wait()



if __name__ == '__main__':
    main(sys.argv[1])
