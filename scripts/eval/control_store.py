"""Atomic evaluation admission beside MLflow; never falls back to local state.

All mutations lock the campaign row. SQL errors are sanitized because connection
strings and dependency errors may contain credentials. Provisioning is explicit.
"""
import contextlib
import decimal
import datetime
import functools
import json
import logging
import os
import re
import tempfile
import time
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


class Contention(RuntimeError):
    """A known-aborted SQL transaction can be retried without paid side effects."""


def retry_contention(operation):
    """Retry SQL-only operations after rollback; never replay connection errors."""
    @functools.wraps(operation)
    def run(*args, **kwargs):
        deadline = time.monotonic() + 30
        delay = 1
        while True:
            try:
                return operation(*args, **kwargs)
            except Contention:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise
                time.sleep(min(delay, remaining))
                delay = min(delay * 2, 10)
    return run


class LeaseLost(RuntimeError):
    pass


class LeaseBusy(RuntimeError):
    pass


def money(value):
    amount = decimal.Decimal(str(value))
    if not amount.is_finite() or amount < 0:
        raise ValueError('USD must be finite and nonnegative')
    return amount


def end_timestamp(value):
    try:
        parsed = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
        if parsed.tzinfo is None:
            raise ValueError('timezone required')
        return parsed.astimezone(datetime.timezone.utc).isoformat()
    except (AttributeError, TypeError, ValueError):
        raise ValueError('end_at must be an ISO timestamp with a timezone') from None


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
        except (psycopg.errors.LockNotAvailable, psycopg.errors.QueryCanceled,
                psycopg.errors.DeadlockDetected, psycopg.errors.SerializationFailure):
            raise Contention('evaluation control transaction timed out or conflicted; retrying') from None
        except psycopg.Error:
            raise Unavailable('evaluation control store unavailable; paid admission refused') from None

    def lock(self, db, name):
        row = db.execute('SELECT policy, stopped FROM eval_control.campaigns WHERE name=%s FOR UPDATE', (name,)).fetchone()
        if row is None:
            raise ValueError('campaign has not been registered')
        return row

    @retry_contention
    def campaign(self, name, policy):
        for kind in ('provider', 'modal'):
            if money(policy[kind + '_daily_usd']) <= 0:
                raise ValueError('daily caps must be positive')
        for kind in ('provider', 'modal'):
            if kind + '_total_usd' in policy and money(policy[kind + '_total_usd']) <= 0:
                raise ValueError('total caps must be positive')
        if 'end_at' in policy:
            end_timestamp(policy['end_at'])
        limit = policy.get('confirmation_limit', 10)
        if type(limit) is not int or not 1 <= limit <= 10:
            raise ValueError('confirmation limit must be 1..10')
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
        """Commit a bounded claim transaction before returning to the caller."""
        keys = lease_batch(keys, ttl)
        return self._claim_many(name, keys, ttl, require_available=require_available)

    @retry_contention
    def _claim_many(self, name, keys, ttl, *, require_available):
        if not keys:
            return {}
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
        return claims

    @contextlib.contextmanager
    def claim_batch(self, name, keys, ttl=3600, *, require_available=False):
        """Validate committed claims without holding a database transaction.

        Release new unpublished owners on caller failure. Start paid work only
        after the entire validation scope exits successfully.
        """
        claims = self.claim_many(name, keys, ttl, require_available=require_available)
        try:
            yield claims
        except BaseException:
            try:
                self.release_many(name, {key: claim['owner'] for key, claim in claims.items()
                                         if claim['status'] == 'claimed'})
            except (Unavailable, Contention):
                logging.getLogger(__name__).warning('unpublished claim release deferred; store unavailable or contended')
            raise

    @retry_contention
    def release_many(self, name, owners):
        """Release only matching unpublished claims; leave replacements intact."""
        keys = lease_batch(owners)
        if not keys:
            return
        with self.transaction() as db:
            self.lock(db, name)
            db.execute('DELETE FROM eval_control.leases AS lease USING unnest(%s::text[],%s::text[]) AS batch(key,owner) WHERE lease.campaign=%s AND lease.key=batch.key AND lease.owner=batch.owner AND lease.payload IS NULL', (keys, [owners[k] for k in keys], name))

    def renew(self, name, key, owner, ttl=3600):
        self.renew_many(name, {key: owner}, ttl)

    @retry_contention
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

    @retry_contention
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

    @retry_contention
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

    def terminal(self, db, name, policy, stopped):
        """Called after the campaign lock; use admission-time UTC, not tx start."""
        if not stopped and policy.get('end_at'):
            if db.execute('SELECT clock_timestamp() >= %s::timestamptz', (policy['end_at'],)).fetchone()[0]:
                stopped = 'campaign end reached'
                db.execute('UPDATE eval_control.campaigns SET stopped=%s WHERE name=%s', (stopped, name))
        return stopped

    @retry_contention
    def stop(self, name, reason='operator stop'):
        # Leaves leases and uncertain spend intact until compute is terminated.
        if reason not in ('operator stop', 'campaign end reached', 'trial limit reached', 'supervisor failed'):
            raise ValueError('unknown campaign stop reason')
        with self.transaction() as db:
            self.lock(db, name)
            db.execute('UPDATE eval_control.campaigns SET stopped=COALESCE(stopped,%s) WHERE name=%s', (reason, name))

    @retry_contention
    def availability(self, name):
        """Read shared lifetime/day state for supervisors and independent watchdogs."""
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            stopped = self.terminal(db, name, policy, stopped)
            day = db.execute("SELECT (clock_timestamp() AT TIME ZONE 'UTC')::date").fetchone()[0]
            rows = db.execute('SELECT kind, sum(charged_usd) FROM eval_control.reservations WHERE campaign=%s GROUP BY kind', (name,)).fetchall()
            totals = dict(rows)
            for kind in ('provider', 'modal'):
                if policy.get(kind + '_total_usd') is not None and totals.get(kind, 0) >= money(policy[kind + '_total_usd']):
                    stopped = stopped or kind + ' total cap reached'
            if stopped:
                db.execute('UPDATE eval_control.campaigns SET stopped=%s WHERE name=%s', (stopped, name))
            daily = db.execute('SELECT kind,sum(charged_usd) FROM eval_control.reservations WHERE campaign=%s AND day=%s GROUP BY kind', (name, day)).fetchall()
            for kind, charged in daily:
                if charged >= money(policy[kind + '_daily_usd']):
                    db.execute('INSERT INTO eval_control.days(campaign,day,kind,stopped) VALUES (%s,%s,%s,true) ON CONFLICT (campaign,day,kind) DO UPDATE SET stopped=true', (name, day, kind))
            paused = db.execute('SELECT kind FROM eval_control.days WHERE campaign=%s AND day=%s AND stopped', (name, day)).fetchall()
            reads = db.execute('SELECT confirmation_reads FROM eval_control.campaigns WHERE name=%s', (name,)).fetchone()[0]
        return {'stopped': stopped, 'paused': bool(paused), 'day': str(day),
                'confirmation_reads_left': policy.get('confirmation_limit', 10) - reads}

    @retry_contention
    def reserve(self, name, kind, usd, metadata=None, lease=None):
        amount, refused = money(usd), False
        if kind not in ('provider', 'modal'):
            raise ValueError('unsupported ledger kind')
        rid = uuid.uuid4().hex
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            if lease:
                self.fence(db, name, *lease)
            stopped = self.terminal(db, name, policy, stopped)
            day = db.execute("SELECT (clock_timestamp() AT TIME ZONE 'UTC')::date").fetchone()[0]
            db.execute('INSERT INTO eval_control.days(campaign,day,kind) VALUES (%s,%s,%s) ON CONFLICT DO NOTHING', (name, day, kind))
            daily_stopped = db.execute('SELECT stopped FROM eval_control.days WHERE campaign=%s AND day=%s AND kind=%s', (name, day, kind)).fetchone()[0]
            used = db.execute('SELECT COALESCE(sum(charged_usd),0) FROM eval_control.reservations WHERE campaign=%s AND day=%s AND kind=%s', (name, day, kind)).fetchone()[0]
            total = db.execute('SELECT COALESCE(sum(charged_usd),0) FROM eval_control.reservations WHERE campaign=%s AND kind=%s', (name, kind)).fetchone()[0]
            if policy.get(kind + '_total_usd') is not None and total + amount > money(policy[kind + '_total_usd']):
                stopped = stopped or kind + ' total cap reached'
                db.execute('UPDATE eval_control.campaigns SET stopped=%s WHERE name=%s', (stopped, name))
            if stopped or daily_stopped or used + amount > money(policy[kind + '_daily_usd']):
                db.execute('UPDATE eval_control.days SET stopped=true WHERE campaign=%s AND day=%s AND kind=%s', (name, day, kind))
                refused = True
            else:
                values = (rid, name, day, kind, amount, amount, json.dumps(metadata or {}, allow_nan=False))
                # Recheck the deadline at the write itself, after preceding SQL
                # and network latency, just like the measurement lease fence.
                predicate = ' WHERE (%s::timestamptz IS NULL OR clock_timestamp()<%s::timestamptz)'
                parameters = (policy.get('end_at'), policy.get('end_at'))
                if lease:
                    predicate += ' AND EXISTS (SELECT 1 FROM eval_control.leases WHERE campaign=%s AND key=%s AND owner=%s AND expires_at>clock_timestamp() AND payload IS NULL)'
                    parameters += (name, *lease)
                row = db.execute('INSERT INTO eval_control.reservations(id,campaign,day,kind,reserved_usd,charged_usd,metadata) SELECT %s,%s,%s,%s,%s,%s,%s::jsonb' + predicate + ' RETURNING id', values + parameters).fetchone()
                if row is None:
                    if self.terminal(db, name, policy, stopped):
                        refused = True
                    else:
                        raise LeaseLost('lease expired before paid admission')
        if refused:
            raise embeddings.BudgetExceeded(kind + ' daily cap reached or campaign stopped')
        return rid

    @retry_contention
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

    @retry_contention
    def policy(self, name):
        """Read frozen admission policy; does not create or replace a campaign."""
        with self.transaction() as db:
            policy, _ = self.lock(db, name)
        return policy

    def evidence(self, name, keys):
        """Read canonical evidence without claiming missing measurement keys."""
        keys = lease_batch(keys)
        return self._evidence(name, keys)

    @retry_contention
    def _evidence(self, name, keys):
        with self.transaction() as db:
            self.lock(db, name)
            rows = db.execute('SELECT key,payload FROM eval_control.leases WHERE campaign=%s AND key=ANY(%s) AND payload IS NOT NULL', (name, keys)).fetchall()
        return dict(rows)

    @retry_contention
    def register_confirmation(self, name, binding):
        encoded = json.dumps(binding, sort_keys=True, allow_nan=False)
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            row = db.execute('SELECT policy FROM eval_control.confirmation_policies WHERE campaign=%s', (name,)).fetchone()
            if row is not None:
                if row[0] != json.loads(encoded):
                    raise ValueError('confirmation policy is immutable')
                # Registration replay is read-only; admission/publication still
                # enforce stop/end under this same campaign-row lock.
                return
            if self.terminal(db, name, policy, stopped):
                raise PermissionError('campaign stopped or ended')
            db.execute('INSERT INTO eval_control.confirmation_policies(campaign,policy) VALUES (%s,%s::jsonb)', (name, encoded))

    @retry_contention
    def confirmation_policy(self, name):
        with self.transaction() as db:
            self.lock(db, name)
            row = db.execute('SELECT policy FROM eval_control.confirmation_policies WHERE campaign=%s', (name,)).fetchone()
        if row is None:
            raise ValueError('confirmation policy has not been registered')
        return row[0]

    @retry_contention
    def confirmation_intent(self, name, intent, owner):
        """Standalone dispatch intent, persisted before Modal app creation."""
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            if self.terminal(db, name, policy, stopped):
                raise LeaseLost('confirmation campaign stopped or ended')
            self.fence(db, name, 'confirmation-resource/' + intent, owner)
            db.execute('INSERT INTO eval_control.confirmation_apps(campaign,intent,owner) VALUES (%s,%s,%s) ON CONFLICT DO NOTHING', (name, intent, owner))

    @retry_contention
    def bind_confirmation_app(self, name, intent, owner, app_id):
        """Bind the fenced, already persisted intent before runner dispatch."""
        if not isinstance(app_id, str) or not re.fullmatch(r'ap-[A-Za-z0-9_-]+', app_id):
            raise ValueError('invalid confirmation app identity')
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            if self.terminal(db, name, policy, stopped):
                raise LeaseLost('confirmation campaign stopped or ended')
            self.fence(db, name, 'confirmation-resource/' + intent, owner)
            row = db.execute('UPDATE eval_control.confirmation_apps SET app_id=%s WHERE campaign=%s AND intent=%s AND owner=%s AND (app_id IS NULL OR app_id=%s) RETURNING app_id', (app_id, name, intent, owner, app_id)).fetchone()
            if row is None:
                raise LeaseLost('confirmation intent missing or already bound')

    @retry_contention
    def confirmation_proof(self, name, key, owner):
        with self.transaction() as db:
            self.lock(db, name)
            row = db.execute('SELECT read_ordinal FROM eval_control.confirmation_reads WHERE campaign=%s AND key=%s AND owner=%s', (name, key, owner)).fetchone()
        if row is None:
            raise PermissionError('protected input was not admitted')
        return {'read_ordinal': row[0]}

    @retry_contention
    def publish_confirmation(self, name, key, owner, payload):
        """Stop/end and the read/owner fence remain atomic at publication."""
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            if self.terminal(db, name, policy, stopped):
                raise LeaseLost('confirmation campaign stopped or ended')
            self.fence(db, name, key, owner)
            if not db.execute('SELECT 1 FROM eval_control.confirmation_reads WHERE campaign=%s AND key=%s AND owner=%s', (name, key, owner)).fetchone():
                raise PermissionError('protected input was not admitted')
            row = db.execute('UPDATE eval_control.leases SET payload=%s::jsonb WHERE campaign=%s AND key=%s AND owner=%s AND expires_at>clock_timestamp() AND payload IS NULL AND (%s::timestamptz IS NULL OR clock_timestamp()<%s::timestamptz) RETURNING key', (json.dumps(payload, allow_nan=False), name, key, owner, policy.get('end_at'), policy.get('end_at'))).fetchone()
            if row is None:
                raise LeaseLost('confirmation ownership or deadline lost before publication')

    @retry_contention
    def confirmation(self, name, lease=None):
        """Atomic guard for trusted confirmation runners; tier 1 never calls it."""
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            if self.terminal(db, name, policy, stopped):
                raise PermissionError('campaign stopped or ended')
            predicate, parameters = '', ()
            if lease is not None:
                self.fence(db, name, *lease)
                if not db.execute('SELECT 1 FROM eval_control.confirmation_policies WHERE campaign=%s', (name,)).fetchone():
                    raise PermissionError('confirmation policy has not been registered')
                if db.execute('SELECT 1 FROM eval_control.confirmation_reads WHERE campaign=%s AND key=%s AND owner=%s', (name, *lease)).fetchone():
                    raise PermissionError('protected input attempt already consumed')
                predicate = ' AND EXISTS (SELECT 1 FROM eval_control.leases WHERE campaign=%s AND key=%s AND owner=%s AND expires_at>clock_timestamp() AND payload IS NULL)'
                parameters = (name, *lease)
            row = db.execute('UPDATE eval_control.campaigns SET confirmation_reads=confirmation_reads+1 WHERE name=%s AND confirmation_reads<%s AND (%s::timestamptz IS NULL OR clock_timestamp()<%s::timestamptz)' + predicate + ' RETURNING confirmation_reads', (name, policy.get('confirmation_limit', 10), policy.get('end_at'), policy.get('end_at')) + parameters).fetchone()
            if row is None:
                if lease is not None:
                    self.fence(db, name, *lease)
                raise PermissionError('campaign confirmation read limit reached')
            if lease is not None:
                db.execute('INSERT INTO eval_control.confirmation_reads(campaign,key,owner,read_ordinal) VALUES (%s,%s,%s,%s)', (name, *lease, row[0]))
            return row[0]

    @retry_contention
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
