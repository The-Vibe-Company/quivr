"""Local fake of the X API v2 endpoints used by the x_list connector.

It serves list timelines (``GET /2/lists/:id/tweets``, newest first, paged by
opaque tokens) and post lookup (``GET /2/tweets?ids=``) from in-memory state
that acceptance tests drive through ``POST /_control/lists/<list_id>``:

    {"posts": [{"id", "text", "created_at", "edit_history_tweet_ids"?, "push"?}],
     "delete": [ids], "protect": [ids], "members": [user ids],
     "fail": {"status": 429|402|..., "reset_in": seconds, "times": n} | null}

For webhook mode it also serves list members (``GET /2/lists/:id/members``),
Filtered Stream rules (``GET|POST /2/tweets/search/stream/rules``), webhooks
(``GET|POST /2/webhooks``, ``PUT /2/webhooks/:id``) and their links to the
stream (``GET /2/tweets/search/webhooks``, ``POST .../:webhook_id``). Creating
or re-validating a webhook sends it a CRC check (``GET <url>?crc_token=``) and
expects ``sha256=`` + base64 HMAC-SHA256 of the token under the app's consumer
secret. A post published with ``"push": true`` is delivered to every valid,
linked webhook whose rules match its author, signed in
``x-twitter-webhooks-signature``; ``"push": "forged"`` signs it with another
secret. The control answer lists each delivery's URL and HTTP status.
``POST /_control/app`` sets ``{"consumer_secret", "crc_fails"}``;
``POST /_control/webhooks`` with ``{"invalidate": true}`` marks every webhook
invalid, which stops deliveries until one passes a CRC check again.

A bearer token starting with ``x-revoked`` is refused with 401. No real X
credential or network access is involved.
"""
import base64, hashlib, hmac, json, secrets, threading, time, urllib.error, urllib.parse, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PAGE_SIZE = 5
# The consumer secret acceptance tests deposit (tests/acceptance/connectors_x_test.go).
CONSUMER_SECRET = 'x-test-consumer-not-real'
USERS = [{'id': '4242', 'username': 'example_desk', 'name': 'Example Desk'}]


class State:
    def __init__(self):
        self.lock = threading.Lock()
        self.lists = {}
        self.posts = {}
        self.gone = {}
        self.consumer_secret = CONSUMER_SECRET
        self.crc_fails = False
        self.rules = []
        self.webhooks = []
        self.linked = set()
        self.next_id = 0

    def list(self, list_id):
        return self.lists.setdefault(list_id, {'ids': set(), 'fail': None, 'members': []})

    def new_id(self, prefix):
        self.next_id += 1
        return f'{prefix}-{self.next_id}'


def _newest_first(ids):
    return sorted(ids, key=lambda i: (len(i), i), reverse=True)


def sign(secret, message):
    return 'sha256=' + base64.b64encode(hmac.new(secret.encode(), message, hashlib.sha256).digest()).decode()


def crc(state, url):
    """Send a CRC check to a webhook URL; True when it answers with the expected token."""
    token = secrets.token_hex(8)
    with state.lock:
        secret = state.consumer_secret
    target = url + ('&' if '?' in url else '?') + urllib.parse.urlencode({'crc_token': token, 'nonce': secrets.token_hex(4)})
    try:
        with urllib.request.urlopen(target, timeout=5) as r:
            answer = json.loads(r.read())
    except (OSError, ValueError, urllib.error.HTTPError):
        return False
    return not state.crc_fails and answer.get('response_token') == sign(secret, token.encode())


def deliver(state, post, forged=False):
    """POST one post, signed, to every valid linked webhook whose rules match its author."""
    with state.lock:
        author = post.get('author_id')
        matched = [r for r in state.rules if author and f'from:{author}' in r['value'].split(' OR ')]
        targets = [w['url'] for w in state.webhooks if w['valid'] and w['id'] in state.linked] if matched else []
        secret = 'x-forged-secret-not-real' if forged else state.consumer_secret
    body = json.dumps({'data': post, 'includes': {'users': USERS}, 'matching_rules': [{'id': r['id'], 'tag': r['tag']} for r in matched]}).encode()
    results = []
    for url in targets:
        req = urllib.request.Request(url, data=body, method='POST', headers={'Content-Type': 'application/json', 'x-twitter-webhooks-signature': sign(secret, body)})
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                status = r.status
        except urllib.error.HTTPError as e:
            status = e.code
        except OSError:
            status = 0
        results.append({'url': url, 'status': status})
    return results


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

        def body(self):
            return json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))) or b'{}')

        def authorized(self):
            auth = self.headers.get('Authorization', '')
            if not auth.startswith('Bearer ') or auth[7:].startswith('x-revoked'):
                self.reply(401, {'title': 'Unauthorized'})
                return False
            return True

        def control(self, path):
            body = self.body()
            if path == '/_control/app':
                with state.lock:
                    state.consumer_secret = body.get('consumer_secret', state.consumer_secret)
                    state.crc_fails = bool(body.get('crc_fails', state.crc_fails))
                return self.reply(200, {'ok': True})
            if path == '/_control/webhooks':
                with state.lock:
                    if body.get('invalidate'):
                        for w in state.webhooks:
                            w['valid'] = False
                return self.reply(200, {'ok': True})
            if not path.startswith('/_control/lists/'):
                return self.reply(404)
            list_id = path.rsplit('/', 1)[1]
            pushed = []
            with state.lock:
                lst = state.list(list_id)
                for post in body.get('posts', []):
                    push = post.pop('push', None)
                    if push == 'forged':
                        # A forged post does not exist at X: it is only delivered.
                        post.setdefault('author_id', '4242')
                        post.setdefault('edit_history_tweet_ids', [post['id']])
                        pushed.append((dict(post), True))
                        continue
                    history = post.setdefault('edit_history_tweet_ids', [post['id']])
                    post.setdefault('author_id', '4242')
                    post.setdefault('lang', 'en')
                    # An edit replaces its earlier versions in timelines; lookup by id
                    # still serves each of them, with the whole edit history, as X does.
                    for old in history[:-1]:
                        lst['ids'].discard(old)
                        if old in state.posts:
                            state.posts[old]['edit_history_tweet_ids'] = history
                    state.posts[post['id']] = post
                    lst['ids'].add(post['id'])
                    if push:
                        pushed.append((dict(post), push == 'forged'))
                for reason, key in (('resource-not-found', 'delete'), ('not-authorized-for-resource', 'protect')):
                    for i in body.get(key, []):
                        state.gone[i] = reason
                        lst['ids'].discard(i)
                if 'members' in body:
                    lst['members'] = list(body['members'])
                if 'fail' in body:
                    lst['fail'] = body['fail']
            # Deliveries leave the lock: the webhook answers after the core relays to the plugin.
            deliveries = [d for post, forged in pushed for d in deliver(state, post, forged)]
            self.reply(200, {'ok': True, 'deliveries': deliveries})

        def do_POST(self):
            path = urllib.parse.urlparse(self.path).path
            if path.startswith('/_control/'):
                return self.control(path)
            if not self.authorized():
                return
            body = self.body()
            if path == '/2/tweets/search/stream/rules':
                with state.lock:
                    for rule in body.get('add', []):
                        state.rules.append({'id': state.new_id('rule'), 'value': rule['value'], 'tag': rule.get('tag', '')})
                    drop = set(body.get('delete', {}).get('ids', []))
                    state.rules = [r for r in state.rules if r['id'] not in drop]
                return self.reply(200, {'meta': {'sent': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}})
            if path == '/2/webhooks':
                if not crc(state, body.get('url', '')):
                    return self.reply(400, {'errors': [{'message': 'CRC validation failed'}]})
                with state.lock:
                    hook = {'id': state.new_id('hook'), 'url': body['url'], 'valid': True}
                    state.webhooks.append(hook)
                return self.reply(200, {'data': hook})
            if path.startswith('/2/tweets/search/webhooks/'):
                with state.lock:
                    state.linked.add(path.rsplit('/', 1)[1])
                return self.reply(200, {'data': {'provisioned': True}})
            self.reply(404, {'title': 'Not Found'})

        def do_PUT(self):
            path = urllib.parse.urlparse(self.path).path
            if not self.authorized():
                return
            if path.startswith('/2/webhooks/'):
                with state.lock:
                    hook = next((w for w in state.webhooks if w['id'] == path.rsplit('/', 1)[1]), None)
                if hook is None:
                    return self.reply(404, {'title': 'Not Found'})
                valid = crc(state, hook['url'])
                with state.lock:
                    hook['valid'] = valid
                return self.reply(200, {'data': dict(hook)})
            self.reply(404, {'title': 'Not Found'})

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
            if not self.authorized():
                return
            with state.lock:
                parts = url.path.split('/')
                if len(parts) == 5 and parts[1:3] == ['2', 'lists'] and parts[4] == 'members':
                    members = state.list(parts[3])['members']
                    return self.reply(200, {'data': [{'id': m, 'username': 'user' + m} for m in members], 'meta': {'result_count': len(members)}})
                if url.path == '/2/tweets/search/stream/rules':
                    return self.reply(200, {'data': state.rules})
                if url.path == '/2/webhooks':
                    return self.reply(200, {'data': state.webhooks})
                if url.path == '/2/tweets/search/webhooks':
                    return self.reply(200, {'data': [{'webhook_id': w} for w in sorted(state.linked)]})
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
                    body = {'meta': meta, 'includes': {'users': USERS}}
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
