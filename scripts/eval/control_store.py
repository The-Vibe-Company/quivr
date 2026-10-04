"""Atomic evaluation admission beside MLflow; never falls back to local state.

All mutations lock the campaign row. SQL errors are sanitized because connection
strings and dependency errors may contain credentials. Provisioning is explicit.
"""
import contextlib
import decimal
import json
import os
import tempfile
import uuid

import embeddings

LEASE_BATCH_SIZE = 128


def lease_batch(keys, ttl=3600):
    keys = list(keys)
    if len(keys) > LEASE_BATCH_SIZE or len(set(keys)) != len(keys):
        raise ValueError('lease batch must contain at most 128 distinct keys')
    if type(ttl) is not int or not 1 <= ttl <= 86400:
        raise ValueError('lease lifetime must be 1..86400 seconds')
    return keys


class Unavailable(RuntimeError):
    pass


class LeaseLost(RuntimeError):
    pass


class LeaseBusy(RuntimeError):
    pass


def money(value):
    amount = decimal.Decimal(str(value))
    if not amount.is_finite() or amount < 0:
        raise ValueError('USD must be finite and nonnegative')
    return amount


class Store:
    def __init__(self, dsn):
        import psycopg
        from psycopg.conninfo import conninfo_to_dict
        self.dsn = dsn
        try:
            info = conninfo_to_dict(dsn)
        except psycopg.Error:
            raise ValueError('invalid evaluation control connection') from None
        self.ca_pem = os.environ.get('EVAL_CONTROL_CA_PEM', '')
        if self.ca_pem:
            if 'sslrootcert' in info:
                raise ValueError('EVAL_CONTROL_CA_PEM conflicts with DSN sslrootcert')
            if info.get('sslmode') != 'verify-full':
                raise ValueError('EVAL_CONTROL_CA_PEM requires sslmode=verify-full')
        host = info.get('hostaddr') or info.get('host')
        if host not in (None, 'localhost', '127.0.0.1', '::1') and not host.startswith('/'):
            if info.get('sslmode') != 'verify-full':
                raise ValueError('remote control store requires sslmode=verify-full')

    @contextlib.contextmanager
    def transaction(self):
        import psycopg
        from psycopg.conninfo import make_conninfo
        try:
            with contextlib.ExitStack() as stack:
                dsn = self.dsn
                if self.ca_pem:
                    try:
                        # NamedTemporaryFile creates mode 0600 and unlinks on every exit.
                        ca = stack.enter_context(tempfile.NamedTemporaryFile(mode='w', encoding='utf-8'))
                        ca.write(self.ca_pem)
                        ca.flush()
                    except (OSError, UnicodeError):
                        raise Unavailable('evaluation control CA unavailable; paid admission refused') from None
                    dsn = make_conninfo(dsn, sslrootcert=ca.name)
                with psycopg.connect(dsn, connect_timeout=5, options='-c statement_timeout=10000') as db:
                    yield db
        except psycopg.Error:
            raise Unavailable('evaluation control store unavailable; paid admission refused') from None

    def lock(self, db, name):
        row = db.execute('SELECT policy, stopped FROM eval_control.campaigns WHERE name=%s FOR UPDATE', (name,)).fetchone()
        if row is None:
            raise ValueError('campaign has not been registered')
        return row

    def campaign(self, name, policy):
        for kind in ('provider', 'modal'):
            if money(policy[kind + '_daily_usd']) <= 0:
                raise ValueError('daily caps must be positive')
        encoded = json.dumps(policy, sort_keys=True, allow_nan=False)
        with self.transaction() as db:
            db.execute('INSERT INTO eval_control.campaigns(name,policy) VALUES (%s,%s::jsonb) ON CONFLICT DO NOTHING', (name, encoded))
            existing, _ = self.lock(db, name)
            if existing != json.loads(encoded):
                raise ValueError('campaign policy is immutable; use a new campaign')

    def fence(self, db, name, key, owner):
        row = db.execute('SELECT owner, expires_at>clock_timestamp(), payload FROM eval_control.leases WHERE campaign=%s AND key=%s', (name, key)).fetchone()
        if not row or row[0] != owner or not row[1] or row[2] is not None:
            raise LeaseLost('lease expired, completed or held by another worker')

    def claim(self, name, key, ttl=3600):
        return self.claim_many(name, [key], ttl)[key]

    def claim_many(self, name, keys, ttl=3600, *, require_available=False):
        with self.claim_batch(name, keys, ttl, require_available=require_available) as claims:
            return claims

    @contextlib.contextmanager
    def claim_batch(self, name, keys, ttl=3600, *, require_available=False):
        """Validate a bounded chunk inside its claim transaction.

        Caller errors roll back new owners. Start paid work only after exit.
        """
        keys = lease_batch(keys, ttl)
        if not keys:
            yield {}
            return
        with self.transaction() as db:
            self.lock(db, name)
            rows = db.execute('SELECT key, owner, expires_at>clock_timestamp(), payload FROM eval_control.leases WHERE campaign=%s AND key=ANY(%s)', (name, keys)).fetchall()
            existing = {row[0]: row[1:] for row in rows}
            claims, owners = {}, {}
            for key in keys:
                row = existing.get(key)
                if row and row[2] is not None:
                    claims[key] = {'status': 'done', 'payload': row[2]}
                elif row and row[1]:
                    if require_available:
                        raise LeaseBusy('lease held by another worker')
                    claims[key] = {'status': 'leased'}
                else:
                    owners[key] = uuid.uuid4().hex
                    claims[key] = {'status': 'claimed', 'owner': owners[key]}
            if owners:
                db.execute("INSERT INTO eval_control.leases(campaign,key,owner,expires_at) SELECT %s, key, owner, clock_timestamp()+%s*interval '1 second' FROM unnest(%s::text[],%s::text[]) AS batch(key,owner) ON CONFLICT (campaign,key) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at", (name, ttl, list(owners), list(owners.values())))
            yield claims

    def renew(self, name, key, owner, ttl=3600):
        self.renew_many(name, {key: owner}, ttl)

    def renew_many(self, name, owners, ttl=3600):
        """Renew the entire chunk or roll back if any entry loses its fence."""
        keys = lease_batch(owners, ttl)
        if not keys:
            return
        with self.transaction() as db:
            self.lock(db, name)
            rows = db.execute("UPDATE eval_control.leases AS lease SET expires_at=clock_timestamp()+%s*interval '1 second' FROM unnest(%s::text[],%s::text[]) AS batch(key,owner) WHERE lease.campaign=%s AND lease.key=batch.key AND lease.owner=batch.owner AND lease.expires_at>clock_timestamp() AND lease.payload IS NULL RETURNING lease.key", (ttl, keys, [owners[k] for k in keys], name)).fetchall()
            if len(rows) != len(keys):
                raise LeaseLost('lease expired, completed or held by another worker')

    def publish(self, name, key, owner, payload):
        self.publish_many(name, {key: (owner, payload)})
        return payload

    def publish_many(self, name, entries):
        """Publish a durable chunk or roll back on any owner/expiry mismatch."""
        keys = lease_batch(entries)
        if not keys:
            return
        owners = [entries[k][0] for k in keys]
        payloads = [json.dumps(entries[k][1], allow_nan=False) for k in keys]
        with self.transaction() as db:
            self.lock(db, name)
            rows = db.execute('UPDATE eval_control.leases AS lease SET payload=batch.payload::jsonb FROM unnest(%s::text[],%s::text[],%s::text[]) AS batch(key,owner,payload) WHERE lease.campaign=%s AND lease.key=batch.key AND lease.owner=batch.owner AND lease.expires_at>clock_timestamp() AND lease.payload IS NULL RETURNING lease.key', (keys, owners, payloads, name)).fetchall()
            if len(rows) != len(keys):
                raise LeaseLost('lease expired, completed or held by another worker')

    def abandon(self, name, key, owner, status):
        """Persist unsuccessful attempt state and immediately allow a later retry."""
        if status not in ('capped', 'failed'):
            raise ValueError('unsupported attempt status')
        with self.transaction() as db:
            self.lock(db, name)
            row = db.execute('UPDATE eval_control.leases SET expires_at=clock_timestamp() WHERE campaign=%s AND key=%s AND owner=%s AND expires_at>clock_timestamp() AND payload IS NULL RETURNING owner', (name, key, owner)).fetchone()
            if row is None:
                raise LeaseLost('lease expired, completed or held by another worker')
            db.execute('INSERT INTO eval_control.attempts(campaign,key,owner,status) VALUES (%s,%s,%s,%s)', (name, key, owner, status))

    def reserve(self, name, kind, usd, metadata=None, lease=None):
        amount, refused = money(usd), False
        if kind not in ('provider', 'modal'):
            raise ValueError('unsupported ledger kind')
        rid = uuid.uuid4().hex
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            if lease:
                self.fence(db, name, *lease)
            day = db.execute("SELECT (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date").fetchone()[0]
            db.execute('INSERT INTO eval_control.days(campaign,day,kind) VALUES (%s,%s,%s) ON CONFLICT DO NOTHING', (name, day, kind))
            daily_stopped = db.execute('SELECT stopped FROM eval_control.days WHERE campaign=%s AND day=%s AND kind=%s', (name, day, kind)).fetchone()[0]
            used = db.execute('SELECT COALESCE(sum(charged_usd),0) FROM eval_control.reservations WHERE campaign=%s AND day=%s AND kind=%s', (name, day, kind)).fetchone()[0]
            if stopped or daily_stopped or used + amount > money(policy[kind + '_daily_usd']):
                db.execute('UPDATE eval_control.days SET stopped=true WHERE campaign=%s AND day=%s AND kind=%s', (name, day, kind))
                refused = True
            else:
                values = (rid, name, day, kind, amount, amount, json.dumps(metadata or {}, allow_nan=False))
                predicate, parameters = ('', ()) if not lease else (
                    ' WHERE EXISTS (SELECT 1 FROM eval_control.leases WHERE campaign=%s AND key=%s AND owner=%s AND expires_at>clock_timestamp() AND payload IS NULL)', (name, *lease))
                row = db.execute('INSERT INTO eval_control.reservations(id,campaign,day,kind,reserved_usd,charged_usd,metadata) SELECT %s,%s,%s,%s,%s,%s,%s::jsonb' + predicate + ' RETURNING id', values + parameters).fetchone()
                if row is None:
                    raise LeaseLost('lease expired before paid admission')
        if refused:
            raise embeddings.BudgetExceeded(kind + ' daily cap reached or campaign stopped')
        return rid

    def settle(self, rid, usd, metadata=None):
        amount, exceeded = money(usd), False
        with self.transaction() as db:
            row = db.execute('SELECT campaign FROM eval_control.reservations WHERE id=%s', (rid,)).fetchone()
            if not row:
                raise ValueError('unknown reservation')
            self.lock(db, row[0])
            reserved, charged, settled = db.execute('SELECT reserved_usd,charged_usd,settled FROM eval_control.reservations WHERE id=%s', (rid,)).fetchone()
            if settled:
                if charged != amount:
                    raise ValueError('conflicting settlement')
                return
            db.execute('UPDATE eval_control.reservations SET charged_usd=%s,settled=true,metadata=metadata||%s::jsonb WHERE id=%s', (amount, json.dumps(metadata or {}, allow_nan=False), rid))
            if amount > reserved:
                db.execute("UPDATE eval_control.campaigns SET stopped='usage exceeded reservation' WHERE name=%s", (row[0],))
                exceeded = True
        if exceeded:
            raise embeddings.BudgetExceeded('confirmed usage exceeded reservation; campaign stopped')

    def confirmation(self, name):
        """Atomic guard for trusted confirmation runners; tier 1 never calls it."""
        with self.transaction() as db:
            self.lock(db, name)
            row = db.execute('UPDATE eval_control.campaigns SET confirmation_reads=confirmation_reads+1 WHERE name=%s AND confirmation_reads<10 RETURNING confirmation_reads', (name,)).fetchone()
            if row is None:
                raise PermissionError('campaign confirmation read limit reached')
            return row[0]

    def summary(self, name):
        with self.transaction() as db:
            self.lock(db, name)
            rows = db.execute('SELECT kind,sum(charged_usd),sum(CASE WHEN settled THEN 0 ELSE charged_usd END) FROM eval_control.reservations WHERE campaign=%s GROUP BY kind', (name,)).fetchall()
        return {kind: {'charged_usd': float(total), 'unknown_usd': float(unknown)} for kind, total, unknown in rows}


class Budget:
    """Hosted's admission protocol backed by a shared campaign ledger."""
    def __init__(self, store, campaign, lease):
        self.store, self.campaign, self.lease = store, campaign, lease
        self.calls = []

    def reserve(self, model, set_name, phase, tokens, price):
        if type(tokens) is not int or tokens <= 0:
            raise ValueError('invalid input-token reservation')
        rate = money(price) / 1_000_000
        self.store.renew(self.campaign, *self.lease)
        rid = self.store.reserve(self.campaign, 'provider', tokens * rate,
                                 {'model': model, 'set': set_name, 'phase': phase, 'reserved_tokens': tokens}, self.lease)
        call = {'id': rid, 'model': model, 'phase': phase, 'reserved': tokens,
                'charged': tokens, 'confirmed': None, 'rate': rate}
        self.calls.append(call)
        return call

    def settle(self, call, tokens):
        if type(tokens) is int and tokens >= 0:
            self.store.settle(call['id'], tokens * call['rate'], {'confirmed_tokens': tokens})
            call.update(charged=tokens, confirmed=tokens)

    def summary(self, model=None, phase=None):
        calls = [c for c in self.calls if (model is None or c['model'] == model) and (phase is None or c['phase'] == phase)]
        return {'confirmed_input_tokens': sum(c['confirmed'] or 0 for c in calls),
                'reserved_input_tokens': sum(c['charged'] for c in calls if c['confirmed'] is None),
                'confirmed_cost_usd': float(sum((c['confirmed'] or 0) * c['rate'] for c in calls)),
                'cost_upper_bound_usd': float(sum(c['charged'] * c['rate'] for c in calls)),
                'admitted_calls': len(calls)}
