"""Durable campaign ownership and compute intents beside the admission ledger."""
import contextlib
import json
import uuid

import control_store
import modal_search
import search_trial


class CleanupPending(RuntimeError):
    pass


class CampaignStore(control_store.Store):
    def register(self, spec, sha, scorer):
        from search_campaign import specification
        spec = specification(spec)
        name = spec['name']
        self.campaign(name, modal_search.frozen_policy(spec['policy'], sha, scorer))
        with self.transaction() as db:
            self.lock(db, name)
            db.execute('INSERT INTO eval_control.campaign_runs(campaign,spec,git_sha,scorer_digest) VALUES (%s,%s::jsonb,%s,%s) ON CONFLICT DO NOTHING',
                       (name, json.dumps(spec), sha, scorer))
            row = db.execute('SELECT spec,git_sha,scorer_digest FROM eval_control.campaign_runs WHERE campaign=%s', (name,)).fetchone()
            if row != (spec, sha, scorer):
                raise ValueError('campaign spec and measurement lineage are immutable')

    def snapshot(self, name):
        with self.transaction() as db:
            self.lock(db, name)
            row = db.execute('SELECT spec,git_sha,scorer_digest,state,owner,expires_at>clock_timestamp(),generation FROM eval_control.campaign_runs WHERE campaign=%s', (name,)).fetchone()
            if not row:
                raise ValueError('campaign supervisor has not been registered')
        return {**row[3], 'spec': row[0], 'git_sha': row[1], 'scorer_digest': row[2],
                'owner': row[4], 'live': bool(row[5]), 'generation': row[6]}

    def confirmation_configuration(self, name, configuration):
        """Freeze validated operator metadata independently of exploration state."""
        with self.edit(name) as (_, state):
            if state.setdefault('confirmation_configuration', configuration) != configuration:
                raise ValueError('confirmation configuration is immutable')

    def development_keys(self, name, trial):
        """Resolve synced aggregate result identities to canonical SQL leases.

        Private query evidence stays in SQL; only opaque lease keys leave this
        boundary. The native runner validates the rows and their MLflow sync.
        """
        import results
        state = self.snapshot(name)
        value = state['trials'][str(trial)]
        evidence = value['report'].get('evidence', [])
        if not evidence or any(item.get('status') != 'synced' for item in evidence):
            raise ValueError('development evidence must be synced')
        identities = {results.result_key(item) for item in evidence}
        configurations = {'baseline': state['spec']['policy']['baseline'], 'candidate': value['config']}
        keys = {dataset: {} for dataset in state['spec']['policy']['sets']}
        with self.transaction() as db:
            self.lock(db, name)
            for side, configuration in configurations.items():
                rows = db.execute("SELECT key,payload FROM eval_control.leases WHERE campaign=%s AND payload->>'tier'='direct' AND payload->'config' @> %s::jsonb ORDER BY key",
                                  (name, json.dumps(configuration))).fetchall()
                for key, row in rows:
                    dataset = row['dataset']['name']
                    if dataset in keys and results.record(row)['result_key'] in identities:
                        keys[dataset].setdefault(side, key)
        if any(set(pair) != {'baseline', 'candidate'} for pair in keys.values()):
            raise ValueError('canonical development pairing is incomplete')
        return keys

    @contextlib.contextmanager
    def edit(self, name):
        """Serialize lead receipts/outboxes, including after compute is stopped.

        This does not grant launch or trial ownership. Those still use mutation.
        """
        with self.transaction() as db:
            self.lock(db, name)
            row = db.execute('SELECT state FROM eval_control.campaign_runs WHERE campaign=%s FOR UPDATE', (name,)).fetchone()
            if row is None:
                raise ValueError('campaign supervisor has not been registered')
            state = row[0]
            yield db, state
            db.execute('UPDATE eval_control.campaign_runs SET state=%s::jsonb WHERE campaign=%s', (json.dumps(state, allow_nan=False), name))

    def acquire(self, name, ttl=120):
        control_store.lease_batch([], ttl)
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            if self.terminal(db, name, policy, stopped):
                raise control_store.LeaseLost('campaign stopped or ended')
            row = db.execute('SELECT owner,expires_at>clock_timestamp(),state FROM eval_control.campaign_runs WHERE campaign=%s FOR UPDATE', (name,)).fetchone()
            if row is None:
                raise ValueError('campaign supervisor has not been registered')
            if row[0] and row[1]:
                raise control_store.LeaseBusy('campaign supervisor is still live')
            if any(r['status'] != 'closed' for r in row[2]['resources'].values()):
                raise CleanupPending('reconcile prior compute before supervisor takeover')
            owner = uuid.uuid4().hex
            db.execute("UPDATE eval_control.campaign_runs SET owner=%s,expires_at=clock_timestamp()+%s*interval '1 second',generation=generation+1 WHERE campaign=%s", (owner, ttl, name))
        return owner

    @contextlib.contextmanager
    def mutation(self, name, owner=None, *, cleanup=False):
        with self.transaction() as db:
            policy, stopped = self.lock(db, name)
            stopped = self.terminal(db, name, policy, stopped)
            row = db.execute('SELECT owner,expires_at>clock_timestamp(),state,generation FROM eval_control.campaign_runs WHERE campaign=%s FOR UPDATE', (name,)).fetchone()
            if not row:
                raise ValueError('campaign supervisor has not been registered')
            if cleanup:
                if not stopped and row[0] and row[1]:
                    raise control_store.LeaseBusy('cannot reconcile compute of a live supervisor')
            elif stopped or not owner or owner != row[0] or not row[1]:
                raise control_store.LeaseLost('supervisor stopped, expired or replaced')
            state = row[2]
            yield db, state, row[3]
            db.execute('UPDATE eval_control.campaign_runs SET state=%s::jsonb WHERE campaign=%s', (json.dumps(state, allow_nan=False), name))

    def renew_owner(self, name, owner, ttl=120):
        control_store.lease_batch([], ttl)
        with self.mutation(name, owner) as (db, _, __):
            db.execute("UPDATE eval_control.campaign_runs SET expires_at=clock_timestamp()+%s*interval '1 second' WHERE campaign=%s", (ttl, name))

    def release_owner(self, name, owner):
        # Release only our ownership; a stopped campaign can still drain safely.
        with self.transaction() as db:
            self.lock(db, name)
            db.execute('UPDATE eval_control.campaign_runs SET expires_at=clock_timestamp() WHERE campaign=%s AND owner=%s', (name, owner))

    def intent(self, name, owner):
        identity = uuid.uuid4().hex
        with self.mutation(name, owner) as (db, state, generation):
            policy, _ = self.lock(db, name)
            # Startup deadline is diagnostic only: AppCreate acknowledgement
            # can be lost independently of a container's startup timeout.
            deadline = db.execute("SELECT clock_timestamp()+%s*interval '1 second'", (policy['startup_seconds'] + 60,)).fetchone()[0]
            resource = {'id': identity, 'label': 'quivr-campaign-' + identity,
                        'generation': generation, 'status': 'pending', 'app_id': None,
                        'creation_deadline': deadline.isoformat()}
            state['resources'][identity] = resource
        return resource

    def bind(self, name, owner, identity, app_id):
        if not isinstance(app_id, str) or not app_id.startswith('ap-'):
            raise ValueError('invalid Modal app identity')
        with self.mutation(name, owner) as (_, state, __):
            resource = state['resources'][identity]
            if resource['status'] == 'closed' or (resource['app_id'] and resource['app_id'] != app_id):
                raise control_store.LeaseLost('resource was already closed or bound')
            resource.update(app_id=app_id, status='running')

    def closed(self, name, identity, *, owner=None):
        with self.mutation(name, owner, cleanup=owner is None) as (_, state, __):
            state['resources'][identity]['status'] = 'closed'

    def trial(self, name, owner, number, value):
        with self.mutation(name, owner) as (_, state, __):
            state['trials'][str(number)] = value

    def clear_leases(self, name):
        with self.mutation(name, cleanup=True) as (db, state, _):
            if any(r['status'] != 'closed' for r in state['resources'].values()):
                raise CleanupPending('compute termination is not acknowledged')
            db.execute('UPDATE eval_control.leases SET expires_at=clock_timestamp() WHERE campaign=%s AND payload IS NULL', (name,))


def cleanup(store, name, compute):
    # Fence before network I/O. New launch/bind is now refused for expired/stopped owners.
    with store.mutation(name, cleanup=True):
        pass
    state = store.snapshot(name)
    pending = False
    for identity, resource in state['resources'].items():
        if resource['status'] == 'closed':
            continue
        try:
            apps = set(compute.find(resource['label']))
            if resource['app_id']:
                apps.add(resource['app_id'])
            # Absence from a listing cannot acknowledge a lost creation RPC.
            # Never permit takeover while such an app might still materialize.
            if not apps:
                pending = True
                continue
            for app in apps:
                compute.stop(app)
                if compute.running(app):
                    raise CleanupPending('Modal termination is not acknowledged')
            store.closed(name, identity)
        except Exception:
            pending = True
    if pending:
        raise CleanupPending('compute cleanup pending; retry watchdog or stop')
    store.clear_leases(name)
