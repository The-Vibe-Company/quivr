"""Atomic evaluation admission beside MLflow; never falls back to local state.

All mutations lock the campaign row. SQL errors are sanitized because connection
strings and dependency errors may contain credentials. Provisioning is explicit.
"""
import contextlib
import decimal
import datetime
import functools
import email.utils
import json
import logging
import os
import re
import tempfile
import time
import uuid
import urllib.error

import embeddings
import network_recovery

LEASE_BATCH_SIZE = 128
VALIDATION_LEASE_TTL = 600


def lease_batch(keys, ttl=3600):
    keys = list(keys)
    if len(keys) > LEASE_BATCH_SIZE or len(set(keys)) != len(keys):
        raise ValueError('lease batch must contain at most 128 distinct keys')
    if type(ttl) is not int or not 1 <= ttl <= 86400:
        raise ValueError('lease lifetime must be 1..86400 seconds')
    return keys


class Unavailable(RuntimeError):
    pass


class NetworkUnavailable(Unavailable, network_recovery.Outage):
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
                with network_recovery.connect(dsn, connect_timeout=5, options='-c statement_timeout=10000') as db:
                    yield db
        except network_recovery.Outage:
            raise NetworkUnavailable('evaluation control connection outage window elapsed; paid admission refused') from None
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

    @retry_contention
    def claim_slot(self, name, prefix, count, ttl, *, lease=None, gate=None):
        """Admit one bounded slot under the campaign lock, fenced by its caller."""
        keys = lease_batch([prefix + (f'/{i}' if count > 1 else '') for i in range(count)], ttl)
        with contextlib.ExitStack() as admission_fence, self.transaction() as db:
            policy, stopped = self.lock(db, name)
            if self.terminal(db, name, policy, stopped):
                raise LeaseLost('campaign stopped or ended')
            if lease:
                self.fence(db, name, *lease)
            occupied = {row[0] for row in db.execute(
                'SELECT key FROM eval_control.leases WHERE campaign=%s AND key=ANY(%s) AND (expires_at>clock_timestamp() OR payload IS NOT NULL)',
                (name, keys)).fetchall()}
            for key in keys:
                if key not in occupied:
                    if gate is not None:
                        admission_fence.enter_context(gate.commit())
                    owner = uuid.uuid4().hex
                    # Recheck parent ownership at the write after SQL round trips.
                    predicate, parameters = '', ()
                    if lease:
                        predicate = ' WHERE EXISTS (SELECT 1 FROM eval_control.leases WHERE campaign=%s AND key=%s AND owner=%s AND expires_at>clock_timestamp() AND payload IS NULL)'
                        parameters = (name, *lease)
                    row = db.execute("INSERT INTO eval_control.leases(campaign,key,owner,expires_at) SELECT %s,%s,%s,clock_timestamp()+%s*interval '1 second'" + predicate +
                        ' ON CONFLICT (campaign,key) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at RETURNING key',
                        (name, key, owner, ttl) + parameters).fetchone()
                    if not row:
                        raise LeaseLost('invocation expired before slot admission')
                    return key, owner
        return None

    @retry_contention
    def provider_slot(self, name, lease, extra_leases=()):
        """All hosted models in one campaign share adaptive request admission."""
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            if self.terminal(db, name, policy, stopped):
                raise LeaseLost('campaign stopped or ended')
            for fence in (lease, *extra_leases):
                self.fence(db, name, *fence)
            db.execute("INSERT INTO eval_control.leases(campaign,key,owner,expires_at,payload) VALUES (%s,'provider-admission','control',clock_timestamp(),%s::jsonb) ON CONFLICT DO NOTHING",
                (name, json.dumps({'limit': 4, 'clean': 0, 'generation': 0})))
            state, cooling = db.execute("SELECT payload,expires_at>clock_timestamp() FROM eval_control.leases WHERE campaign=%s AND key='provider-admission'", (name,)).fetchone()
            active = {row[0] for row in db.execute("SELECT key FROM eval_control.leases WHERE campaign=%s AND key LIKE 'provider-request/%%' AND expires_at>clock_timestamp()", (name,)).fetchall()}
            if cooling or len(active) >= state['limit']:
                return None
            key = next('provider-request/' + str(i) for i in range(4) if 'provider-request/' + str(i) not in active)
            owner = uuid.uuid4().hex
            # Only the bounded Modal callers install ProviderAdmission. Socket
            # timeouts do not bound a streaming response: retain the permit for
            # a full invocation lifetime, beyond its hard container deadline.
            ttl = policy.get('max_seconds', 3600) + policy.get('startup_seconds', 0)
            lease_batch([], ttl)
            predicates = ['EXISTS (SELECT 1 FROM eval_control.leases WHERE campaign=%s AND key=%s AND owner=%s AND expires_at>clock_timestamp() AND payload IS NULL)' for _ in (lease, *extra_leases)]
            parameters = tuple(value for fence in (lease, *extra_leases) for value in (name, *fence))
            row = db.execute("INSERT INTO eval_control.leases(campaign,key,owner,expires_at) SELECT %s,%s,%s,clock_timestamp()+%s*interval '1 second' WHERE " + ' AND '.join(predicates) +
                ' ON CONFLICT (campaign,key) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at RETURNING key', (name, key, owner, ttl) + parameters).fetchone()
            if not row:
                raise LeaseLost('invocation or latency window expired before provider admission')
            return key, owner, state['generation']

    @retry_contention
    def release_slot(self, name, slot):
        """Expire our unpublished slot using the documented UPDATE grant."""
        with self.transaction() as db:
            self.lock(db, name)
            db.execute('UPDATE eval_control.leases SET expires_at=clock_timestamp() WHERE campaign=%s AND key=%s AND owner=%s AND payload IS NULL', (name, *slot))

    @retry_contention
    def provider_feedback(self, name, permit, error):
        with self.transaction() as db:
            self.lock(db, name)
            key, owner, generation = permit
            row = db.execute('UPDATE eval_control.leases SET expires_at=clock_timestamp() WHERE campaign=%s AND key=%s AND owner=%s AND expires_at>clock_timestamp() AND payload IS NULL RETURNING key', (name, key, owner)).fetchone()
            if not row:
                return  # A replacement permit owns feedback now.
            state = db.execute("SELECT payload FROM eval_control.leases WHERE campaign=%s AND key='provider-admission'", (name,)).fetchone()[0]
            delay = 0
            if isinstance(error, urllib.error.HTTPError) and error.code == 429:
                state.update(limit=max(1, state['limit'] // 2), clean=0, generation=state['generation'] + 1)
                try:
                    retry_after = error.headers.get('Retry-After', '1')
                    delay = (float(retry_after) if retry_after.isdigit() else
                             email.utils.parsedate_to_datetime(retry_after).timestamp() - time.time())
                except (AttributeError, TypeError, ValueError, OverflowError):
                    delay = 1
                delay = max(1, min(60, delay))
            elif error is not None:
                state['clean'] = 0
            elif generation == state['generation']:
                state['clean'] += 1
                if state['clean'] >= 16 * state['limit'] and state['limit'] < 4:
                    state.update(limit=state['limit'] + 1, clean=0)
            db.execute("UPDATE eval_control.leases SET payload=%s::jsonb,expires_at=GREATEST(expires_at,clock_timestamp()+%s*interval '1 second') WHERE campaign=%s AND key='provider-admission'",
                (json.dumps(state), delay, name))

    @contextlib.contextmanager
    def latency_window(self, name, lease, ttl):
        """Wait outside timing, then hold the fresh paired serving window only."""
        slot = None
        try:
            while slot is None:
                self.renew(name, *lease, ttl=ttl)
                slot = self.claim_slot(name, 'campaign-latency-slot', 1, ttl, lease=lease)
                if slot is None:
                    time.sleep(1)
            yield slot
        finally:
            # An outage can leave an in-flight call behind. Retain its bounded
            # fence; never spend another outage window on compensating writes.
            import sys
            if slot and not isinstance(sys.exc_info()[1], network_recovery.Outage):
                self.release_slot(name, slot)

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
        after the entire validation scope exits successfully. Unpaid reservations
        expire within ten minutes if compensation cannot reach the store.
        """
        lease_batch([], ttl)
        claims = self.claim_many(name, keys, min(ttl, VALIDATION_LEASE_TTL), require_available=require_available)
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
    def reserve(self, name, kind, usd, metadata=None, lease=None, *, extra_leases=()):
        gate = network_recovery.admission(name)
        while True:
            gate.wait()
            try:
                return self._reserve(name, kind, usd, metadata, lease, gate, extra_leases)
            except network_recovery.AdmissionPaused:
                continue

    def _reserve(self, name, kind, usd, metadata, lease, gate, extra_leases):
        amount, refused = money(usd), False
        if kind not in ('provider', 'modal'):
            raise ValueError('unsupported ledger kind')
        rid = uuid.uuid4().hex
        leases = ([lease] if lease else []) + list(extra_leases)
        with contextlib.ExitStack() as admission_fence, self.transaction() as db:
            policy, stopped = self.lock(db, name)
            for fence in leases:
                self.fence(db, name, *fence)
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
                admission_fence.enter_context(gate.commit())
                values = (rid, name, day, kind, amount, amount, json.dumps(metadata or {}, allow_nan=False))
                # Recheck the deadline at the write itself, after preceding SQL
                # and network latency, just like the measurement lease fence.
                predicate = ' WHERE (%s::timestamptz IS NULL OR clock_timestamp()<%s::timestamptz)'
                parameters = (policy.get('end_at'), policy.get('end_at'))
                for fence in leases:
                    predicate += ' AND EXISTS (SELECT 1 FROM eval_control.leases WHERE campaign=%s AND key=%s AND owner=%s AND expires_at>clock_timestamp() AND payload IS NULL)'
                    parameters += (name, *fence)
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
    def start_modal_attempt(self, rid, lease):
        """Infrastructure restarts reserve again before doing measurement work.

        An interrupted attempt keeps its full unknown charge. Modal can retry
        preempted inputs even when application retries are disabled.
        """
        with self.transaction() as db:
            row = db.execute('SELECT campaign FROM eval_control.reservations WHERE id=%s AND kind=\'modal\'', (rid,)).fetchone()
            if not row:
                raise ValueError('unknown Modal reservation')
            name = row[0]
            policy, stopped = self.lock(db, name)
            if self.terminal(db, name, policy, stopped):
                raise embeddings.BudgetExceeded('campaign stopped or ended')
            self.fence(db, name, *lease)
            amount, metadata, settled = db.execute('SELECT reserved_usd,metadata,settled FROM eval_control.reservations WHERE id=%s', (rid,)).fetchone()
            for fence in metadata.get('extra_leases', []):
                self.fence(db, name, *fence)
            if settled:
                raise ValueError('Modal reservation already settled')
            if not metadata.get('execution_started'):
                db.execute('UPDATE eval_control.reservations SET metadata=metadata||\'{"execution_started":true}\'::jsonb WHERE id=%s', (rid,))
                return rid
        return self.reserve(name, 'modal', amount, {**metadata, 'restarted_from': rid}, lease,
                            extra_leases=metadata.get('extra_leases', []))

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
    def reusable_evidence(self, name, source, key):
        """Read a completed measurement from an explicitly selected campaign.

        Admission limits may change; every measurement-affecting policy field
        and its code/scorer identity must still match. Stopped sources are valid.
        """
        scheduling = ['provider_daily_usd', 'modal_daily_usd', 'provider_total_usd', 'modal_total_usd',
                      'max_seconds', 'startup_seconds', 'end_at', 'confirmation_limit',
                      'agent_token_usage', 'reuse_campaign']
        with self.transaction() as db:
            current, _ = self.lock(db, name)
            row = db.execute('SELECT l.payload FROM eval_control.leases l JOIN eval_control.campaigns c ON c.name=l.campaign WHERE l.campaign=%s AND l.key=%s AND l.payload IS NOT NULL AND c.policy-%s::text[]=%s::jsonb-%s::text[]',
                             (source, key, scheduling, json.dumps(current), scheduling)).fetchone()
            if row is None:
                return None
            payload = row[0]
            pair = payload.get('provenance', {}).get('private_pair') or payload.get('provenance', {}).get('public_pair')
            if pair and not db.execute('SELECT 1 FROM eval_control.leases WHERE campaign=%s AND key=%s AND payload IS NOT NULL',
                                       (source, pair['baseline_lease_key'])).fetchone():
                return None
        return payload

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


class ProviderAdmission:
    """Cross-container Hosted request context; SQL waits stay outside HTTP timing."""
    def __init__(self, budget):
        self.budget = budget
        self.store, self.campaign, self.lease = budget.store, budget.campaign, budget.lease

    @contextlib.contextmanager
    def request(self):
        permit, error = None, None
        try:
            while permit is None:
                self.store.renew_many(self.campaign, dict([self.lease, *self.budget.extra_leases]), ttl=self.budget.ttl)
                permit = self.store.provider_slot(self.campaign, self.lease, self.budget.extra_leases)
                if permit is None:
                    time.sleep(1)
            yield
        except BaseException as caught:
            error = caught
            raise
        finally:
            if permit and not isinstance(error, network_recovery.Outage):
                self.store.provider_feedback(self.campaign, permit, error)


class Budget:
    """Hosted's admission protocol backed by a shared campaign ledger."""
    def __init__(self, store, campaign, lease, ttl=3600):
        self.store, self.campaign, self.lease = store, campaign, lease
        self.ttl = ttl
        self.calls = []
        self.extra_leases = []

    def reserve(self, model, set_name, phase, tokens, price):
        if type(tokens) is not int or tokens <= 0:
            raise ValueError('invalid input-token reservation')
        rate = money(price) / 1_000_000
        if self.extra_leases:
            self.store.renew_many(self.campaign, dict([self.lease, *self.extra_leases]), ttl=self.ttl)
        else:
            self.store.renew(self.campaign, *self.lease, ttl=self.ttl)
        rid = self.store.reserve(self.campaign, 'provider', tokens * rate,
                                 {'model': model, 'set': set_name, 'phase': phase, 'reserved_tokens': tokens}, self.lease, extra_leases=self.extra_leases)
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
