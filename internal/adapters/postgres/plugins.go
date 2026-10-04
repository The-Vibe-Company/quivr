package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PluginStore keeps the plugin registry and the active Pipeline Plan.
type PluginStore struct{ Pool *pgxpool.Pool }

var _ registry.Store = PluginStore{}

// pluginPlanLock serializes every change of the active plan: the startup
// configuration of api and worker, and activations.
const pluginPlanLock = 642002

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// pinnedWorkSQL counts the unfinished work pinned to a plan that names the
// registration (THE-782). A connector run lasts at most its three bounded
// attempts, so a run pinned for longer than connectorRunPinExpiry lost its
// release with a worker that died during its last attempt and no longer
// counts.
const pinnedWorkSQL = `(SELECT count(*) FROM pipeline_plan_work w WHERE (w.kind<>'connector_run' OR w.pinned_at>now()-interval '` + connectorRunPinExpiry + `')
 AND (w.ingestion_registration_id IS NULL OR NOT ('ingestion'=ANY(plugin_registrations.contributions)) OR w.ingestion_registration_id=plugin_registrations.id)
 AND EXISTS (SELECT 1 FROM pipeline_plan_roles pr WHERE pr.plan_id=w.plan_id AND pr.registration_id=plugin_registrations.id))`

// evaluatorSQL keeps the Subscription Versions v pinning the evaluator of an
// alert-rule registration: its plugin id and version (THE-805).
const evaluatorSQL = `'subscription'=ANY(plugin_registrations.contributions)
 AND v.evaluator->>'plugin_id'=plugin_registrations.plugin_id AND v.evaluator->>'version'=plugin_registrations.version`

// pendingEvaluationsSQL counts the pending evaluations of Subscription
// Versions pinning an alert-rule registration's version: work it must still
// judge, whichever plan is active.
const pendingEvaluationsSQL = `(SELECT count(*) FROM evaluation_intents i JOIN subscription_versions v ON v.organization=i.organization AND v.id=i.subscription_version_id
 WHERE i.state='pending' AND i.kind='evaluation' AND ` + evaluatorSQL + `)`

// undispatchedSQL counts the earlier Subscription Versions pinning an
// alert-rule registration's version that changes not yet dispatched may still
// reach: a Version judges the changes committed before its successor's
// activation, and evaluation work for them exists only once the
// Organization's dispatch checkpoint passed that activation. A deleted
// Subscription judges nothing.
const undispatchedSQL = `(SELECT count(*) FROM subscription_versions v JOIN subscriptions s ON s.organization=v.organization AND s.id=v.subscription_id
 LEFT JOIN monitoring_checkpoints c ON c.organization=v.organization
 WHERE NOT s.deleted AND s.current_version_id<>v.id AND ` + evaluatorSQL + `
 AND (SELECT min(n.activation_position) FROM subscription_versions n WHERE n.organization=v.organization AND n.subscription_id=v.subscription_id AND n.activation_position>v.activation_position)>coalesce(c.position,0))`

// subscriptionsSQL counts the Subscriptions that are not deleted whose current
// Version pins an alert-rule registration's version.
const subscriptionsSQL = `(SELECT count(*) FROM subscription_versions v JOIN subscriptions s ON s.organization=v.organization AND s.id=v.subscription_id AND s.current_version_id=v.id
 WHERE NOT s.deleted AND ` + evaluatorSQL + `)`

// connectorRunPinExpiry is well past a connector run's longest life: three
// attempts of at most five minutes and their backoff.
const connectorRunPinExpiry = "30 minutes"

// registrationColumns read a registration. A registration a plan change left
// out is draining while work pinned to a plan naming it remains or, for an
// alert-rule plugin, while Subscriptions pin its version, and inactive once
// none does: derived when read, so no process has to notice the last piece of
// work finishing.
const registrationColumns = `id,plugin_id,version,endpoint,manifest_digest,coalesce(artifact_digest,''),contributions,roles,
 CASE WHEN state IN ('draining','inactive') THEN CASE WHEN ` + pinnedWorkSQL + `+` + pendingEvaluationsSQL + `+` + undispatchedSQL + `+` + subscriptionsSQL + `>0 THEN 'draining' ELSE 'inactive' END ELSE state END,
 created_at,updated_at,manifest,settings,check_report,origin,` + pinnedWorkSQL + `+` + pendingEvaluationsSQL + `+` + undispatchedSQL + `,` + subscriptionsSQL

// scanRegistration scans registrationColumns, then extra columns into extra.
func scanRegistration(row pgx.Row, extra ...any) (registry.Registration, error) {
	var r registry.Registration
	var settings, report []byte
	err := row.Scan(append([]any{&r.ID, &r.PluginID, &r.Version, &r.Endpoint, &r.ManifestDigest, &r.ArtifactDigest, &r.Contributions, &r.Roles, &r.State, &r.CreatedAt, &r.UpdatedAt, &r.Manifest, &settings, &report, &r.Origin, &r.PinnedWork, &r.Subscriptions}, extra...)...)
	if err != nil {
		return r, err
	}
	if len(settings) > 0 {
		if err = json.Unmarshal(settings, &r.Settings); err != nil {
			return r, err
		}
	}
	if len(report) > 0 {
		r.Check = &registry.CheckReport{}
		err = json.Unmarshal(report, r.Check)
	}
	return r, err
}

func registrations(ctx context.Context, q querier, where string, args ...any) ([]registry.Registration, error) {
	rows, err := q.Query(ctx, `SELECT `+registrationColumns+` FROM plugin_registrations `+where+` ORDER BY created_at,plugin_id,version,id`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (registry.Registration, error) { return scanRegistration(row) })
}

func insertRegistration(ctx context.Context, q querier, r registry.Registration) error {
	settings, err := json.Marshal(r.Settings)
	if err != nil {
		return err
	}
	origin := r.Origin
	if origin == "" {
		origin = registry.OriginConfiguration
	}
	fixtures, err := fixturesColumn(r.Fixtures)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO plugin_registrations(id,plugin_id,version,endpoint,manifest_digest,artifact_digest,contributions,roles,state,manifest,settings,origin,check_fixtures) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (id) DO NOTHING`,
		r.ID, r.PluginID, r.Version, r.Endpoint, r.ManifestDigest, r.ArtifactDigest, r.Contributions, r.Roles, r.State, r.Manifest, settings, origin, fixtures)
	return err
}

// fixturesColumn is a fixture set as stored: a JSON object of base64
// contents by path, NULL for none.
func fixturesColumn(files map[string][]byte) ([]byte, error) {
	if len(files) == 0 {
		return nil, nil
	}
	return json.Marshal(files)
}

// ApplyConfiguration records the configured registrations and applies the
// roles the configuration changed since it last applied (registry.Reconcile)
// as a new plan, under the plan lock api and worker share at startup. A
// configuration without pins on a registry without a plan writes nothing.
func (s PluginStore) ApplyConfiguration(ctx context.Context, seed registry.Seed) (registry.Applied, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return registry.Applied{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", pluginPlanLock); err != nil {
		return registry.Applied{}, err
	}
	active, members, err := activeMembers(ctx, tx)
	var current *registry.Plan
	switch {
	case errors.Is(err, registry.ErrNoPlan):
		if len(seed.Registrations) == 0 {
			return registry.Applied{}, nil
		}
	case err != nil:
		return registry.Applied{}, err
	default:
		current = &active
	}
	for _, r := range seed.Registrations {
		r.State = registry.StateInactive
		if err = insertRegistration(ctx, tx, r); err != nil {
			return registry.Applied{}, err
		}
		if _, known := members[r.ID]; !known {
			stored, err := scanRegistration(tx.QueryRow(ctx, `SELECT `+registrationColumns+` FROM plugin_registrations WHERE id=$1`, r.ID))
			if err != nil {
				return registry.Applied{}, err
			}
			members[r.ID] = stored
		}
	}
	var snapshot map[string]string
	var raw []byte
	switch err = tx.QueryRow(ctx, `SELECT roles FROM pipeline_configuration`).Scan(&raw); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return registry.Applied{}, err
	default:
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			return registry.Applied{}, err
		}
	}
	applied := registry.Applied{Reconciliation: registry.Reconcile(seed, snapshot, current, members)}
	if current != nil {
		applied.Plan = current.ID
	}
	if applied.Changed {
		if applied.Plan, err = recordPlan(ctx, tx, registry.SourceConfiguration, applied.Plan, applied.Roles); err != nil {
			return registry.Applied{}, err
		}
	}
	configured, err := json.Marshal(seed.Snapshot())
	if err != nil {
		return registry.Applied{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO pipeline_configuration(roles) VALUES($1) ON CONFLICT (singleton) DO UPDATE SET roles=EXCLUDED.roles, applied_at=now() WHERE pipeline_configuration.roles<>EXCLUDED.roles`, configured); err != nil {
		return registry.Applied{}, err
	}
	return applied, tx.Commit(ctx)
}

// recordPlan writes a new plan replacing previous ("" when none is active),
// makes it active, and moves registrations in and out of the active state.
func recordPlan(ctx context.Context, tx pgx.Tx, source, previous string, roles []registry.Assignment) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	plan := "plan_" + hex.EncodeToString(id[:])
	if _, err := tx.Exec(ctx, "INSERT INTO pipeline_plans(id,source,previous_plan_id) VALUES($1,$2,NULLIF($3,''))", plan, source, previous); err != nil {
		return "", err
	}
	members := []string{}
	for _, a := range roles {
		if _, err := tx.Exec(ctx, "INSERT INTO pipeline_plan_roles(plan_id,role,registration_id) VALUES($1,$2,$3)", plan, a.Role, a.RegistrationID); err != nil {
			return "", err
		}
		members = append(members, a.RegistrationID)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO active_pipeline_plan(plan_id) VALUES($1) ON CONFLICT (singleton) DO UPDATE SET plan_id=EXCLUDED.plan_id, activated_at=now()", plan); err != nil {
		return "", err
	}
	// A registration that leaves the plan drains: it reads as draining while
	// work pinned to a plan naming it remains, and inactive afterwards
	// (registrationColumns).
	if _, err := tx.Exec(ctx, `UPDATE plugin_registrations SET state=CASE WHEN id=ANY($1) THEN 'active' ELSE 'draining' END, updated_at=now()
 WHERE (id=ANY($1) AND state<>'active') OR (NOT id=ANY($1) AND state='active')`, members); err != nil {
		return "", err
	}
	return plan, nil
}

// PluginRegistrations lists every registration, oldest first.
func (s PluginStore) PluginRegistrations(ctx context.Context) ([]registry.Registration, error) {
	return registrations(ctx, s.Pool, "")
}

// PluginRegistration returns one registration, or registry.ErrNotFound.
func (s PluginStore) PluginRegistration(ctx context.Context, id string) (registry.Registration, error) {
	r, err := scanRegistration(s.Pool.QueryRow(ctx, `SELECT `+registrationColumns+` FROM plugin_registrations WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, registry.ErrNotFound
	}
	return r, err
}

// ActivePlan returns the active Pipeline Plan with its roles sorted, or
// registry.ErrNoPlan.
func (s PluginStore) ActivePlan(ctx context.Context) (registry.Plan, error) {
	return activePlan(ctx, s.Pool)
}

func activePlan(ctx context.Context, q querier) (registry.Plan, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT plan_id FROM active_pipeline_plan`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return registry.Plan{}, registry.ErrNoPlan
	}
	if err != nil {
		return registry.Plan{}, err
	}
	return pipelinePlan(ctx, q, id)
}

// PipelinePlan returns any recorded plan, or registry.ErrNotFound. The
// activation time of a plan that is no longer active is when it was last
// activated as far as the registry knows: its creation.
func (s PluginStore) PipelinePlan(ctx context.Context, id string) (registry.Plan, error) {
	return pipelinePlan(ctx, s.Pool, id)
}

func pipelinePlan(ctx context.Context, q querier, id string) (registry.Plan, error) {
	var plan registry.Plan
	err := q.QueryRow(ctx, `SELECT p.id,p.created_at,coalesce(a.activated_at,p.created_at),p.source,coalesce(p.previous_plan_id,'') FROM pipeline_plans p LEFT JOIN active_pipeline_plan a ON a.plan_id=p.id WHERE p.id=$1`, id).Scan(&plan.ID, &plan.CreatedAt, &plan.ActivatedAt, &plan.Source, &plan.PreviousPlanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return plan, registry.ErrNotFound
	}
	if err != nil {
		return plan, err
	}
	rows, err := q.Query(ctx, `SELECT pr.role,pr.registration_id,r.plugin_id,r.version FROM pipeline_plan_roles pr JOIN plugin_registrations r ON r.id=pr.registration_id WHERE pr.plan_id=$1 ORDER BY pr.role`, plan.ID)
	if err != nil {
		return plan, err
	}
	plan.Roles, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (registry.Assignment, error) {
		var a registry.Assignment
		err := row.Scan(&a.Role, &a.RegistrationID, &a.PluginID, &a.Version)
		return a, err
	})
	return plan, err
}

// ActivePlanID is the active plan's id, or "" when none is: the one read a
// process polls to follow plan changes.
func (s PluginStore) ActivePlanID(ctx context.Context) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT plan_id FROM active_pipeline_plan`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// PlanMembers returns any recorded plan and its registrations by id, or
// registry.ErrNotFound: work pinned to a plan resolves it (THE-782).
func (s PluginStore) PlanMembers(ctx context.Context, id string) (registry.Plan, map[string]registry.Registration, error) {
	members := map[string]registry.Registration{}
	plan, err := pipelinePlan(ctx, s.Pool, id)
	if err != nil {
		return plan, members, err
	}
	list, err := registrations(ctx, s.Pool, `WHERE id IN (SELECT registration_id FROM pipeline_plan_roles WHERE plan_id=$1)`, plan.ID)
	for _, r := range list {
		members[r.ID] = r
	}
	return plan, members, err
}

// EvaluatorRegistrations returns every registration a plan named for an
// alert-rule role, ordered by the latest plan naming each, oldest first.
func (s PluginStore) EvaluatorRegistrations(ctx context.Context) ([]registry.Registration, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+registrationColumns+` FROM plugin_registrations
 JOIN (SELECT pr.registration_id,max(p.created_at) AS named_at FROM pipeline_plan_roles pr JOIN pipeline_plans p ON p.id=pr.plan_id
  WHERE pr.role LIKE 'subscription:%' GROUP BY pr.registration_id) named ON named.registration_id=plugin_registrations.id
 ORDER BY named.named_at,plugin_registrations.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (registry.Registration, error) { return scanRegistration(row) })
}

// PinWork pins a piece of work to plan unless it is pinned already, and
// returns the plan it is pinned to: its first attempt's, whatever the active
// plan is when it retries. stopped reports that a rollback stopped the work
// (registry.PinnedWorkStop). A connector run pinned past
// connectorRunPinExpiry no longer counts as draining work, so a run
// dispatched again under the same identity is pinned again, to plan.
func (s PluginStore) PinWork(ctx context.Context, kind, org, id, plan string) (pinned string, stopped bool, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return "", false, err
	}
	err = tx.QueryRow(ctx, `WITH pinned AS (INSERT INTO pipeline_plan_work(kind,organization,work_id,plan_id) VALUES($1,$2,$3,CASE WHEN $1='ingestion' THEN COALESCE((SELECT plan_id FROM active_pipeline_plan),$4) ELSE $4 END)
 ON CONFLICT (kind,organization,work_id) DO UPDATE SET plan_id=EXCLUDED.plan_id,pinned_at=now(),unavailable_attempts=0,stopped_at=NULL
 WHERE pipeline_plan_work.kind='connector_run' AND pipeline_plan_work.pinned_at<=now()-interval '`+connectorRunPinExpiry+`' RETURNING plan_id,stopped_at IS NOT NULL)
	 SELECT * FROM pinned UNION ALL SELECT plan_id,stopped_at IS NOT NULL FROM pipeline_plan_work WHERE kind=$1 AND organization=$2 AND work_id=$3 LIMIT 1`, kind, org, id, plan).Scan(&pinned, &stopped)
	if err != nil {
		return pinned, stopped, err
	}
	return pinned, stopped, tx.Commit(ctx)
}

// ReleaseWork forgets a finished piece of work; a registration it kept
// draining reads as inactive once no other work holds it.
func (s PluginStore) ReleaseWork(ctx context.Context, kind, org, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM pipeline_plan_work WHERE kind=$1 AND organization=$2 AND work_id=$3`, kind, org, id)
	return err
}

// CountUnavailable counts one attempt of a piece of work that found a plugin
// of its plan unreachable, and returns the work's count.
func (s PluginStore) CountUnavailable(ctx context.Context, kind, org, id string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `UPDATE pipeline_plan_work SET unavailable_attempts=unavailable_attempts+1 WHERE kind=$1 AND organization=$2 AND work_id=$3 RETURNING unavailable_attempts`, kind, org, id).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%s %s is not pinned to a plan", kind, id)
	}
	return n, err
}

// WorkStopped reports whether a rollback stopped a piece of work
// (registry.PinnedWorkStop); work that is not pinned is not stopped.
func (s PluginStore) WorkStopped(ctx context.Context, kind, org, id string) (bool, error) {
	var stopped bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pipeline_plan_work WHERE kind=$1 AND organization=$2 AND work_id=$3 AND stopped_at IS NOT NULL)`, kind, org, id).Scan(&stopped)
	return stopped, err
}

// ActiveMembers returns the active plan and its registrations by id.
func (s PluginStore) ActiveMembers(ctx context.Context) (registry.Plan, map[string]registry.Registration, error) {
	return activeMembers(ctx, s.Pool)
}

func activeMembers(ctx context.Context, q querier) (registry.Plan, map[string]registry.Registration, error) {
	members := map[string]registry.Registration{}
	plan, err := activePlan(ctx, q)
	if err != nil {
		return plan, members, err
	}
	list, err := registrations(ctx, q, `WHERE id IN (SELECT registration_id FROM pipeline_plan_roles WHERE plan_id=$1)`, plan.ID)
	for _, r := range list {
		members[r.ID] = r
	}
	return plan, members, err
}

// RegisterPlugin records a registration under an idempotency key.
func (s PluginStore) RegisterPlugin(ctx context.Context, r registry.Registration, key string) (registry.Registration, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return registry.Registration{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", pluginPlanLock); err != nil {
		return registry.Registration{}, false, err
	}
	var keyed, keyedFixtures string
	digest := registry.FixturesDigest(r.Fixtures)
	switch err = tx.QueryRow(ctx, `SELECT registration_id,fixtures_digest FROM plugin_registration_requests WHERE request_key=$1`, key).Scan(&keyed, &keyedFixtures); {
	case err == nil && (keyed != r.ID || keyedFixtures != digest):
		return registry.Registration{}, false, registry.ErrIdempotencyConflict
	case err == nil:
		stored, err := scanRegistration(tx.QueryRow(ctx, `SELECT `+registrationColumns+` FROM plugin_registrations WHERE id=$1`, r.ID))
		return stored, false, err
	case !errors.Is(err, pgx.ErrNoRows):
		return registry.Registration{}, false, err
	}
	if err = insertRegistration(ctx, tx, r); err != nil {
		return registry.Registration{}, false, err
	}
	// A new request for a rejected registration checks it again, with the
	// request's fixtures.
	fixtures, err := fixturesColumn(r.Fixtures)
	if err != nil {
		return registry.Registration{}, false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE plugin_registrations SET state='registered',check_report=NULL,check_fixtures=$2,check_lease_until='-infinity',updated_at=now() WHERE id=$1 AND state='rejected'`, r.ID, fixtures)
	if err != nil {
		return registry.Registration{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO plugin_registration_requests(request_key,registration_id,fixtures_digest) VALUES($1,$2,$3)`, key, r.ID, digest); err != nil {
		return registry.Registration{}, false, err
	}
	stored, err := scanRegistration(tx.QueryRow(ctx, `SELECT `+registrationColumns+` FROM plugin_registrations WHERE id=$1`, r.ID))
	if err != nil {
		return stored, false, err
	}
	return stored, stored.State == registry.StateRegistered || tag.RowsAffected() > 0, tx.Commit(ctx)
}

// ClaimCheck leases the oldest registration waiting for its check, with its
// fixtures.
func (s PluginStore) ClaimCheck(ctx context.Context, lease time.Duration) (registry.Registration, bool, error) {
	var fixtures []byte
	r, err := scanRegistration(s.Pool.QueryRow(ctx, `UPDATE plugin_registrations SET check_lease_until=now()+$1::interval WHERE id=(
 SELECT id FROM plugin_registrations WHERE state='registered' AND check_lease_until<now() ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 RETURNING `+registrationColumns+`,check_fixtures`, fmt.Sprintf("%d milliseconds", lease.Milliseconds())), &fixtures)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err == nil && len(fixtures) > 0 {
		err = json.Unmarshal(fixtures, &r.Fixtures)
	}
	return r, err == nil, err
}

// RecordCheck stores the report and settles a registration still waiting for
// it as validated or rejected.
func (s PluginStore) RecordCheck(ctx context.Context, id string, report registry.CheckReport) error {
	b, err := json.Marshal(report)
	if err != nil {
		return err
	}
	state := registry.StateRejected
	if report.Certified {
		state = registry.StateValidated
	}
	_, err = s.Pool.Exec(ctx, `UPDATE plugin_registrations SET state=$2,check_report=$3,check_lease_until='-infinity',updated_at=now() WHERE id=$1 AND state='registered'`, id, state, b)
	return err
}

// Activate records the plan decide returns for a registration, with its
// vector spaces, under the plan lock. Activating the registration that
// already serves every role it declares returns the active plan unchanged.
func (s PluginStore) Activate(ctx context.Context, id string, decide func(registry.Plan, map[string]registry.Registration, registry.Registration) (registry.Activation, error)) (registry.Plan, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return registry.Plan{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", pluginPlanLock); err != nil {
		return registry.Plan{}, err
	}
	target, err := scanRegistration(tx.QueryRow(ctx, `SELECT `+registrationColumns+` FROM plugin_registrations WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return registry.Plan{}, registry.ErrNotFound
	}
	if err != nil {
		return registry.Plan{}, err
	}
	active, members, err := activeMembers(ctx, tx)
	if err != nil && !errors.Is(err, registry.ErrNoPlan) {
		return registry.Plan{}, err
	}
	activation, err := decide(active, members, target)
	if err != nil {
		return registry.Plan{}, err
	}
	if activation.Unchanged {
		return active, tx.Commit(ctx)
	}
	routingChanged, err := applyIngestionRouting(ctx, tx, active, activation)
	if err != nil {
		return registry.Plan{}, err
	}
	if activation.Spaces != nil {
		if err = registerSpaces(ctx, tx, activation.Spaces); err != nil {
			return registry.Plan{}, err
		}
	}
	plan, err := recordPlan(ctx, tx, registry.SourceActivation, active.ID, activation.Roles)
	if err != nil {
		return registry.Plan{}, err
	}
	if routingChanged {
		if err = queuePendingServingProjections(ctx, tx, active, activation); err != nil {
			return registry.Plan{}, err
		}
	}
	out, err := pipelinePlan(ctx, tx, plan)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// PipelinePlans returns the latest limit plans, newest first.
func (s PluginStore) PipelinePlans(ctx context.Context, limit int) ([]registry.Plan, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM pipeline_plans ORDER BY created_at DESC,id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	plans := make([]registry.Plan, 0, len(ids))
	for _, id := range ids {
		plan, err := pipelinePlan(ctx, s.Pool, id)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// Rollback records the plan decide returns for the rollback target under the
// plan lock, with its vector spaces, and with Stop marks the work pinned to a
// plan naming a registration that leaves the plan. The request's key replays
// the plan it recorded.
func (s PluginStore) Rollback(ctx context.Context, req registry.RollbackRequest, decide func(active, target registry.Plan, members map[string]registry.Registration) (registry.Activation, error)) (registry.Plan, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return registry.Plan{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", pluginPlanLock); err != nil {
		return registry.Plan{}, err
	}
	request, err := json.Marshal(map[string]string{"plan_id": req.Plan, "pinned_work": req.PinnedWork})
	if err != nil {
		return registry.Plan{}, err
	}
	var keyed string
	var same bool
	switch err = tx.QueryRow(ctx, `SELECT plan_id,request=$2::jsonb FROM pipeline_plan_requests WHERE request_key=$1`, req.Key, request).Scan(&keyed, &same); {
	case err == nil && !same:
		return registry.Plan{}, registry.ErrIdempotencyConflict
	case err == nil:
		return pipelinePlan(ctx, tx, keyed)
	case !errors.Is(err, pgx.ErrNoRows):
		return registry.Plan{}, err
	}
	active, members, err := activeMembers(ctx, tx)
	if errors.Is(err, registry.ErrNoPlan) {
		return registry.Plan{}, registry.ErrNoPreviousPlan
	}
	if err != nil {
		return registry.Plan{}, err
	}
	targetID := req.Plan
	if targetID == "" {
		if targetID = active.PreviousPlanID; targetID == "" {
			return registry.Plan{}, registry.ErrNoPreviousPlan
		}
	}
	target, err := pipelinePlan(ctx, tx, targetID)
	if err != nil {
		return registry.Plan{}, err
	}
	list, err := registrations(ctx, tx, `WHERE id IN (SELECT registration_id FROM pipeline_plan_roles WHERE plan_id=$1)`, target.ID)
	if err != nil {
		return registry.Plan{}, err
	}
	for _, r := range list {
		members[r.ID] = r
	}
	a, err := decide(active, target, members)
	if err != nil {
		return registry.Plan{}, err
	}
	plan := active.ID
	if !a.Unchanged {
		routingChanged, err := applyIngestionRouting(ctx, tx, active, a)
		if err != nil {
			return registry.Plan{}, err
		}
		if a.Spaces != nil {
			if err = registerSpaces(ctx, tx, a.Spaces); err != nil {
				return registry.Plan{}, err
			}
		}
		if plan, err = recordPlan(ctx, tx, registry.SourceRollback, active.ID, a.Roles); err != nil {
			return registry.Plan{}, err
		}
		if routingChanged {
			if err = queuePendingServingProjections(ctx, tx, active, a); err != nil {
				return registry.Plan{}, err
			}
		}
		if req.Stop() && routingChanged {
			if err = stopOutgoingIngestion(ctx, tx, active, a); err != nil {
				return registry.Plan{}, err
			}
		}
		if req.Stop() && len(a.Retired) > 0 {
			if _, err = tx.Exec(ctx, `UPDATE pipeline_plan_work SET stopped_at=now() WHERE stopped_at IS NULL
 AND plan_id IN (SELECT plan_id FROM pipeline_plan_roles WHERE registration_id=ANY($1))`, a.Retired); err != nil {
				return registry.Plan{}, err
			}
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO pipeline_plan_requests(request_key,request,plan_id) VALUES($1,$2,$3)`, req.Key, request, plan); err != nil {
		return registry.Plan{}, err
	}
	out, err := pipelinePlan(ctx, tx, plan)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// BindIngestionWork names a receipt's routed owner; retries cannot move it.
func (s PluginStore) BindIngestionWork(ctx context.Context, kind, org, id, registration string) error {
	if kind != "ingestion" {
		return nil
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE pipeline_plan_work w SET ingestion_registration_id=$4
 WHERE kind=$1 AND organization=$2 AND work_id=$3 AND (ingestion_registration_id IS NULL OR ingestion_registration_id=$4)
	 AND EXISTS(SELECT 1 FROM pipeline_plan_roles r WHERE r.plan_id=w.plan_id AND r.registration_id=$4 AND (r.role IN ('ingestion','ingestion-default') OR r.role LIKE 'ingestion:%' OR r.role LIKE 'ingestion-route:%' OR r.role LIKE 'ingestion-evaluation:%'))`, kind, org, id, registration)
	if err == nil && tag.RowsAffected() != 1 {
		return registry.ErrConflict
	}
	return err
}

// CountPluginUnavailable counts only attempts to the failed registration.
func (s PluginStore) CountPluginUnavailable(ctx context.Context, kind, org, id, registration string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `UPDATE pipeline_plan_work SET plugin_unavailable_attempts=jsonb_set(plugin_unavailable_attempts,ARRAY[$4::text],to_jsonb(COALESCE((plugin_unavailable_attempts->>$4)::int,0)+1))
 WHERE kind=$1 AND organization=$2 AND work_id=$3 RETURNING (plugin_unavailable_attempts->>$4)::int`, kind, org, id, registration).Scan(&n)
	return n, err
}

// IngestionSpaceOwner resolves retained declared spaces through their recorded
// owner, including a registration that has drained from the active plan.
func (s PluginStore) IngestionSpaceOwner(ctx context.Context, space string) (*plugins.Pin, error) {
	r, err := scanRegistration(s.Pool.QueryRow(ctx, `SELECT `+registrationColumns+` FROM plugin_registrations WHERE plugin_id=(SELECT owner_plugin_id FROM vector_spaces WHERE id=$1) AND version=(SELECT owner_plugin_version FROM vector_spaces WHERE id=$1)
 AND EXISTS(SELECT 1 FROM pipeline_plan_roles r WHERE r.registration_id=plugin_registrations.id AND (r.role='ingestion' OR r.role LIKE 'ingestion:%'))
 ORDER BY (state='active') DESC,(state='draining') DESC,created_at DESC,id LIMIT 1`, space))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pin, err := r.Pin()
	if err != nil {
		return nil, err
	}
	if pin.Manifest.Contributions.Ingestion == nil {
		return nil, nil
	}
	for name, declared := range pin.Manifest.Contributions.Ingestion.Spaces {
		if plugins.SpaceKey(name, declared.Version) == space {
			return pin, nil
		}
	}
	return nil, nil
}
