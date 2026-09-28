"""Local fake of the Microsoft identity platform and Microsoft Graph mail APIs.

It serves just what the m365_mail connector uses, with Graph's shapes:
- POST /{tenant}/oauth2/v2.0/token (client secret or certificate assertion)
- GET  /v1.0/users/{mailbox}/mailFolders/{folder}/messages/delta
  (initial round with a receivedDateTime filter, skip and delta tokens,
  Prefer: odata.maxpagesize, @removed entries, 410 when a delta token expired)
- GET  /v1.0/users/{mailbox}/messages/{id}[/attachments[/{id}/$value]]

Tests drive it through a small control API under /_fake/ (JSON bodies). State
is partitioned by mailbox, so every test uses its own mailbox. Nothing here is
a real tenant, mailbox or secret.
"""
import base64, calendar, http.server, json, threading, time, urllib.parse

MAX_PAGE = 10


class State:
    def __init__(self):
        self.lock = threading.Lock()
        self.apps = {}        # client_id -> {'secret': str|None, 'expired': bool, 'certificate': bool}
        self.tokens = {}      # access token -> client_id
        self.issued = 0
        self.issued_by = {}   # client_id -> tokens issued
        self.mailboxes = {}   # mailbox -> messages, change log, attachments, scripted failures, request counts

    def mailbox(self, name):
        return self.mailboxes.setdefault(name.lower(), {'messages': {}, 'order': [], 'log': [], 'seq': 0, 'files': {}, 'fail': [], 'expire': False,
                                                        'delay': 0.0,
                                                        'requests': {'delta': 0, 'attachments': 0, 'value': 0, 'resyncs': 0}})


def parse_iso(s):
    return calendar.timegm(time.strptime(s, '%Y-%m-%dT%H:%M:%SZ'))


class Handler(http.server.BaseHTTPRequestHandler):
    state: State = None

    def log_message(self, *_):
        pass

    def reply(self, status, body=None, headers=None, raw=None):
        payload = raw if raw is not None else json.dumps(body if body is not None else {}).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/octet-stream' if raw is not None else 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(payload)

    def body(self):
        n = int(self.headers.get('Content-Length') or 0)
        return self.rfile.read(n) if n else b''

    def graph_error(self, status, code, headers=None):
        self.reply(status, {'error': {'code': code, 'message': 'fake graph'}}, headers)

    # ---- control API -------------------------------------------------
    def do_POST(self):
        path = urllib.parse.urlparse(self.path).path
        if path.endswith('/oauth2/v2.0/token'):
            return self.token()
        if not path.startswith('/_fake/'):
            return self.graph_error(404, 'ResourceNotFound')
        data = json.loads(self.body() or b'{}')
        s = self.state
        with s.lock:
            if path == '/_fake/apps':
                s.apps[data['client_id']] = {'secret': data.get('secret'), 'expired': bool(data.get('expired')), 'certificate': bool(data.get('certificate'))}
            elif path == '/_fake/messages':
                box = s.mailbox(data['mailbox'])
                msg = self.message(data)
                if msg['id'] not in box['messages']:
                    box['order'].append(msg['id'])
                box['messages'][msg['id']] = msg
                box['files'][msg['id']] = {a['id']: a for a in data.get('attachments') or []}
                box['seq'] += 1
                box['log'].append((box['seq'], msg['id']))
            elif path == '/_fake/update':
                box = s.mailbox(data['mailbox'])
                msg = box['messages'][data['id']]
                msg.update(data.get('fields', {}))
                box['seq'] += 1
                box['log'].append((box['seq'], data['id']))
            elif path == '/_fake/delete':
                box = s.mailbox(data['mailbox'])
                box['messages'].pop(data['id'], None)
                box['seq'] += 1
                box['log'].append((box['seq'], data['id']))
            elif path == '/_fake/fail':
                box = s.mailbox(data['mailbox'])
                box['fail'] += [dict(status=data['status'], code=data.get('code', ''), retry_after=data.get('retry_after'), path=data.get('path', ''))] * int(data.get('count', 1))
            elif path == '/_fake/clear-failures':
                s.mailbox(data['mailbox'])['fail'] = []
            elif path == '/_fake/expire-delta':
                s.mailbox(data['mailbox'])['expire'] = True
            elif path == '/_fake/delay':
                s.mailbox(data['mailbox'])['delay'] = float(data['seconds'])
            else:
                return self.graph_error(404, 'ResourceNotFound')
        self.reply(200, {'ok': True})

    def message(self, data):
        body = data.get('html')
        return {'id': data['id'], 'internetMessageId': data.get('internet_message_id', '<%s@example.org>' % data['id']),
                'subject': data.get('subject', ''), 'body': {'contentType': 'html' if body is not None else 'text', 'content': body if body is not None else data.get('text', '')},
                'from': {'emailAddress': {'name': 'Desk', 'address': 'desk@example.org'}}, 'sender': {'emailAddress': {'name': 'Desk', 'address': 'desk@example.org'}},
                'toRecipients': [{'emailAddress': {'name': 'Monitoring', 'address': data['mailbox']}}], 'ccRecipients': [],
                'sentDateTime': data['received'], 'receivedDateTime': data['received'], 'conversationId': 'conv-' + data['id'],
                'hasAttachments': bool(data.get('attachments') or []), 'isRead': False}

    # ---- identity platform --------------------------------------------
    def token(self):
        form = urllib.parse.parse_qs(self.body().decode())
        client = (form.get('client_id') or [''])[0]
        s = self.state
        with s.lock:
            app = s.apps.get(client)
            if app is None:
                return self.reply(400, {'error': 'unauthorized_client', 'error_codes': [700016]})
            if app['expired']:
                return self.reply(401, {'error': 'invalid_client', 'error_codes': [7000222]})
            if app['certificate']:
                ok = (form.get('client_assertion_type') or [''])[0] == 'urn:ietf:params:oauth:client-assertion-type:jwt-bearer' and len((form.get('client_assertion') or [''])[0].split('.')) == 3
            else:
                ok = (form.get('client_secret') or [''])[0] == app['secret']
            if not ok:
                return self.reply(401, {'error': 'invalid_client', 'error_codes': [7000215]})
            s.issued += 1
            value = 'fake-access-%d' % s.issued
            s.tokens[value] = client
            s.issued_by[client] = s.issued_by.get(client, 0) + 1
        self.reply(200, {'token_type': 'Bearer', 'expires_in': 3599, 'access_token': value})

    # ---- Graph --------------------------------------------------------
    def do_GET(self):
        url = urllib.parse.urlparse(self.path)
        if url.path == '/_fake/stats':
            q = urllib.parse.parse_qs(url.query)
            with self.state.lock:
                box = self.state.mailbox(q['mailbox'][0])
                return self.reply(200, dict(box['requests'], tokens_issued=self.state.issued_by.get(q.get('client_id', [''])[0], 0)))
        parts = [urllib.parse.unquote(p) for p in url.path.split('/') if p]
        if len(parts) < 3 or parts[0] != 'v1.0' or parts[1] != 'users':
            return self.graph_error(404, 'ResourceNotFound')
        s = self.state
        with s.lock:
            auth = self.headers.get('Authorization', '')
            if not auth.startswith('Bearer ') or auth[7:] not in s.tokens:
                return self.graph_error(401, 'InvalidAuthenticationToken')
            if parts[2].lower().startswith('unknown'):
                return self.graph_error(404, 'ErrorInvalidUser')
            box = s.mailbox(parts[2])
            for i, f in enumerate(box['fail']):
                if f['path'] in url.path:
                    box['fail'].pop(i)
                    return self.graph_error(f['status'], f['code'], {'Retry-After': str(f['retry_after'])} if f['retry_after'] is not None else None)
            rest = parts[3:]
            if len(rest) == 4 and rest[0] == 'mailFolders' and rest[2:] == ['messages', 'delta']:
                box['requests']['delta'] += 1
                delay = box['delay']
            else:
                delay = None
        if delay is not None:
            # Slow pages outside the lock so restart tests can stop a run midway.
            time.sleep(delay)
            with s.lock:
                return self.delta(box, url)
        with s.lock:
            rest = parts[3:]
            if len(rest) >= 2 and rest[0] == 'messages':
                msg = box['messages'].get(rest[1])
                if msg is None:
                    return self.graph_error(404, 'ErrorItemNotFound')
                if len(rest) == 2:
                    return self.reply(200, msg)
                files = box['files'].get(rest[1], {})
                if len(rest) == 3 and rest[2] == 'attachments':
                    box['requests']['attachments'] += 1
                    return self.reply(200, {'value': [self.attachment_meta(a) for a in files.values()]})
                if len(rest) == 5 and rest[2] == 'attachments' and rest[4] == '$value':
                    att = files.get(rest[3])
                    if att is None:
                        return self.graph_error(404, 'ErrorItemNotFound')
                    if att.get('type', 'file') == 'reference':
                        return self.graph_error(405, 'ErrorInvalidRequest')
                    box['requests']['value'] += 1
                    return self.reply(200, raw=base64.b64decode(att.get('data_b64', '')))
        self.graph_error(404, 'ResourceNotFound')

    @staticmethod
    def attachment_meta(a):
        kind = {'file': 'fileAttachment', 'item': 'itemAttachment', 'reference': 'referenceAttachment'}[a.get('type', 'file')]
        size = a.get('size') or len(base64.b64decode(a.get('data_b64', '')))
        return {'@odata.type': '#microsoft.graph.' + kind, 'id': a['id'], 'name': a.get('name', a['id']), 'contentType': a.get('content_type', 'application/octet-stream'),
                'size': size, 'isInline': False, 'lastModifiedDateTime': '2026-01-01T00:00:00Z'}

    def delta(self, box, url):
        q = {k: v[0] for k, v in urllib.parse.parse_qs(url.query).items()}
        prefer = self.headers.get('Prefer', '')
        size = MAX_PAGE
        if 'odata.maxpagesize=' in prefer:
            size = int(prefer.split('odata.maxpagesize=')[1].split(',')[0].strip())
        flt = q.get('$filter', '')
        since = parse_iso(flt.split(' ge ')[1]) if ' ge ' in flt else 0
        base = 'http://%s%s?' % (self.headers['Host'], url.path)
        keep = {'$filter': flt} if flt else {}
        if '$deltatoken' in q and box['expire']:
            box['expire'] = False
            box['requests']['resyncs'] += 1
            return self.graph_error(410, 'syncStateNotFound')
        if '$skiptoken' in q:
            mode, start, offset = q['$skiptoken'].split(':')
            start, offset = int(start), int(offset)
        elif '$deltatoken' in q:
            mode, start, offset = 'd', int(q['$deltatoken']), 0
        else:
            mode, start, offset = 'i', box['seq'], 0
        if mode == 'i':
            ids = [i for i in box['order'] if i in box['messages']]
        else:
            ids = []
            for seq, mid in box['log']:
                if start < seq <= box['seq'] and mid not in ids:
                    ids.append(mid)
        entries = []
        for mid in ids:
            msg = box['messages'].get(mid)
            if msg is None:
                entries.append({'id': mid, '@removed': {'reason': 'deleted'}})
            elif parse_iso(msg['receivedDateTime']) >= since:
                entries.append(msg)
        page = entries[offset:offset + size]
        out = {'value': page}
        # An initial round ends at the snapshot it started from: later changes
        # surface in the next round (a possible replay, as with Graph).
        end = start if mode == 'i' else max(start, box['seq'])
        if offset + size < len(entries):
            out['@odata.nextLink'] = base + urllib.parse.urlencode(dict(keep, **{'$skiptoken': '%s:%d:%d' % (mode, start, offset + size)}))
        else:
            out['@odata.deltaLink'] = base + urllib.parse.urlencode(dict(keep, **{'$deltatoken': str(end)}))
        self.reply(200, out)


class FakeGraph:
    """Runs the fake in a daemon thread of the calling process."""

    def __init__(self, port):
        handler = type('BoundHandler', (Handler,), {'state': State()})
        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', port), handler)
        self.url = 'http://127.0.0.1:%d' % port
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
