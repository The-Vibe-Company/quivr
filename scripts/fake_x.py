"""Local fake of the X API v2 endpoints used by the x_list connector.

It serves list timelines (``GET /2/lists/:id/tweets``, newest first, paged by
opaque tokens) and post lookup (``GET /2/tweets?ids=``) from in-memory state
that acceptance tests drive through ``POST /_control/lists/<list_id>``:

    {"posts": [{"id", "text", "created_at", "edit_history_tweet_ids"?}],
     "delete": [ids], "protect": [ids],
     "fail": {"status": 429|402|..., "reset_in": seconds, "times": n} | null}

A bearer token starting with ``x-revoked`` is refused with 401. No real X
credential or network access is involved.
"""
import json, threading, time, urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PAGE_SIZE = 5


class State:
    def __init__(self):
        self.lock = threading.Lock()
        self.lists = {}
        self.posts = {}
        self.gone = {}

    def list(self, list_id):
        return self.lists.setdefault(list_id, {'ids': set(), 'fail': None})


def _newest_first(ids):
    return sorted(ids, key=lambda i: (len(i), i), reverse=True)


def handler(state):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def reply(self, status, body=None, headers=None):
            raw = json.dumps(body).encode() if body is not None else b''
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(raw)))
            for k, v in (headers or {}).items():
                self.send_header(k, v)
            self.end_headers()
            self.wfile.write(raw)

        def do_POST(self):
            path = urllib.parse.urlparse(self.path).path
            if not path.startswith('/_control/lists/'):
                return self.reply(404)
            list_id = path.rsplit('/', 1)[1]
            body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))) or b'{}')
            with state.lock:
                lst = state.list(list_id)
                for post in body.get('posts', []):
                    history = post.setdefault('edit_history_tweet_ids', [post['id']])
                    post.setdefault('author_id', '4242')
                    post.setdefault('lang', 'en')
                    for old in history[:-1]:
                        lst['ids'].discard(old)
                        state.posts.pop(old, None)
                    state.posts[post['id']] = post
                    lst['ids'].add(post['id'])
                for reason, key in (('resource-not-found', 'delete'), ('not-authorized-for-resource', 'protect')):
                    for i in body.get(key, []):
                        state.gone[i] = reason
                        lst['ids'].discard(i)
                if 'fail' in body:
                    lst['fail'] = body['fail']
            self.reply(200, {'ok': True})

        def failure(self, lst):
            fail = lst['fail']
            if not fail:
                return False
            if fail.get('times') is not None:
                fail['times'] -= 1
                if fail['times'] <= 0:
                    lst['fail'] = None
            headers = {}
            if fail.get('reset_in') is not None:
                headers['x-rate-limit-reset'] = str(int(time.time()) + int(fail['reset_in']))
            kind = 'credits-depleted' if fail['status'] == 402 else 'failure'
            self.reply(fail['status'], {'title': 'Failure', 'type': 'https://api.x.com/2/problems/' + kind}, headers)
            return True

        def do_GET(self):
            url = urllib.parse.urlparse(self.path)
            q = urllib.parse.parse_qs(url.query)
            auth = self.headers.get('Authorization', '')
            if not auth.startswith('Bearer ') or auth[7:].startswith('x-revoked'):
                return self.reply(401, {'title': 'Unauthorized'})
            with state.lock:
                parts = url.path.split('/')
                if len(parts) == 5 and parts[1:3] == ['2', 'lists'] and parts[4] == 'tweets':
                    lst = state.list(parts[3])
                    if self.failure(lst):
                        return
                    if 'edit_history_tweet_ids' not in q.get('tweet.fields', [''])[0]:
                        return self.reply(400, {'title': 'Invalid Request'})
                    ids = _newest_first(lst['ids'])
                    token = q.get('pagination_token', [''])[0]
                    start = 0
                    if token:
                        if not token.startswith('p') or not token[1:].isdigit():
                            return self.reply(400, {'title': 'Invalid Request'})
                        start = int(token[1:])
                    size = min(int(q.get('max_results', ['100'])[0]), PAGE_SIZE)
                    page = ids[start:start + size]
                    meta = {'result_count': len(page)}
                    if start + size < len(ids):
                        meta['next_token'] = 'p%d' % (start + size)
                    body = {'meta': meta, 'includes': {'users': [{'id': '4242', 'username': 'example_desk', 'name': 'Example Desk'}]}}
                    if page:
                        body['data'] = [state.posts[i] for i in page]
                    return self.reply(200, body)
                if url.path == '/2/tweets':
                    data, errors = [], []
                    for i in q.get('ids', [''])[0].split(','):
                        if i in state.gone or i not in state.posts:
                            errors.append({'value': i, 'resource_id': i, 'resource_type': 'tweet', 'parameter': 'ids',
                                           'type': 'https://api.twitter.com/2/problems/' + state.gone.get(i, 'resource-not-found')})
                        else:
                            data.append({'id': i, 'edit_history_tweet_ids': state.posts[i]['edit_history_tweet_ids']})
                    body = {}
                    if data:
                        body['data'] = data
                    if errors:
                        body['errors'] = errors
                    return self.reply(200, body)
            self.reply(404, {'title': 'Not Found'})

    return Handler


def start(port):
    """Serve the fake on 127.0.0.1:port in a daemon thread; returns the server."""
    server = ThreadingHTTPServer(('127.0.0.1', port), handler(State()))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server
