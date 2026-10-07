package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnectorStore persists Connector Instances, their Deposited Credentials,
// schedule leases and Acquisition Checkpoints. Mutations that change public
// state lock the Organization journal first, then the instance row, and
// commit their change events in the same transaction.
type ConnectorStore struct{ Pool *pgxpool.Pool }

const connectorColumns = `c.organization,c.id,c.corpus_id,c.source_namespace,c.kind,c.config,c.interval_seconds,c.silent_after_seconds,c.credential_warning_seconds,c.enabled,c.created_at,c.disabled_at,
c.health_state,c.health_evaluated_at,c.last_success_at,c.last_item_at,c.last_error_code,c.last_error_class,c.last_error_at,c.access_error_at,
k.version,k.deposited_at,k.expires_at,
CASE WHEN c.usage_day IS NULL THEN NULL ELSE ` + utcToday + ` END,
CASE WHEN c.usage_day=` + utcToday + ` THEN c.usage_items ELSE 0 END,
CASE WHEN c.usage_day=` + utcToday + ` THEN c.usage_previous_items WHEN c.usage_day=` + utcToday + `-1 THEN c.usage_items ELSE 0 END,
c.diagnostics,
c.push_state,c.push_setup_class,c.push_setup_code,c.push_setup_at,c.push_poll_interval_seconds,c.push_error_class,c.push_error_code,c.push_error_at,c.push_last_delivery_at,c.push_policy`

// utcToday is the current UTC calendar day, the window of usage counters.
const utcToday = `(now() AT TIME ZONE 'UTC')::date`

const connectorFrom = ` FROM connector_instances c LEFT JOIN LATERAL (
  SELECT version,deposited_at,expires_at FROM connector_credentials WHERE organization=c.organization AND connector_id=c.id ORDER BY version DESC LIMIT 1
) k ON true`

func scanConnector(row pgx.Row) (connectors.Instance, error) {
	var in connectors.Instance
	var interval, silent, warning int64
	var code, class *string
	var errorAt *time.Time
	var version *int
	var deposited, expires, usageDay *time.Time
	var usageToday, usagePrevious int64
	var diagnostics, pushPolicy []byte
	var pushState, setupClass, setupCode, pushClass, pushCode *string
	var setupAt, pushAt, lastDelivery *time.Time
	var pollInterval *int64
	err := row.Scan(&in.Organization, &in.ID, &in.CorpusID, &in.Namespace, &in.Kind, &in.Config, &interval, &silent, &warning, &in.Enabled, &in.CreatedAt, &in.DisabledAt,
		&in.Health.State, &in.Health.EvaluatedAt, &in.Health.LastSuccessAt, &in.Health.LastItemAt, &code, &class, &errorAt, &in.Health.AccessErrorAt, &version, &deposited, &expires,
		&usageDay, &usageToday, &usagePrevious, &diagnostics,
		&pushState, &setupClass, &setupCode, &setupAt, &pollInterval, &pushClass, &pushCode, &pushAt, &lastDelivery, &pushPolicy)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, corpus.ErrNotFound
	}
	if err != nil {
		return in, err
	}
	if len(pushPolicy) > 0 {
		if err := json.Unmarshal(pushPolicy, &in.PushPolicy); err != nil {
			return in, err
		}
	}
	in.Interval, in.SilentAfter, in.CredentialWarning = time.Duration(interval)*time.Second, time.Duration(silent)*time.Second, time.Duration(warning)*time.Second
	if code != nil && errorAt != nil {
		in.Health.LastError = &connectors.RunError{Code: *code, At: *errorAt}
		if class != nil {
			in.Health.LastError.Class = connectors.ErrorClass(*class)
		}
	}
	if version != nil {
		in.Credential = &connectors.CredentialInfo{Version: *version, DepositedAt: *deposited, ExpiresAt: expires}
	}
	if usageDay != nil {
		in.Health.Usage = &connectors.Usage{Day: *usageDay, ItemsRead: usageToday, PreviousDayItemsRead: usagePrevious}
	}
	if len(diagnostics) > 0 {
		in.Health.Diagnostics = diagnostics
	}
	if pushState != nil {
		push := &connectors.PushHealth{Setup: *pushState, LastDeliveryAt: lastDelivery}
		if setupCode != nil && setupAt != nil {
			push.SetupError = &connectors.RunError{Code: *setupCode, At: *setupAt}
			if setupClass != nil {
				push.SetupError.Class = connectors.ErrorClass(*setupClass)
			}
		}
		if pollInterval != nil {
			push.PollInterval = time.Duration(*pollInterval) * time.Second
		}
		if pushCode != nil && pushAt != nil && pushClass != nil {
			push.DeliveryError = &connectors.RunError{Class: connectors.ErrorClass(*pushClass), Code: *pushCode, At: *pushAt}
		}
		in.Health.Push = push
	}
	return in, nil
}

func readConnector(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, org, id string, lock bool) (connectors.Instance, error) {
	query := "SELECT " + connectorColumns + connectorFrom + " WHERE c.organization=$1 AND c.id=$2"
	if lock {
		query += " FOR UPDATE OF c"
	}
	return scanConnector(q.QueryRow(ctx, query, org, id))
}

func healthInput(in connectors.Instance) connectors.HealthInput {
	h := connectors.HealthInput{Enabled: in.Enabled, CreatedAt: in.CreatedAt, LastSuccessAt: in.Health.LastSuccessAt, LastItemAt: in.Health.LastItemAt, LastError: in.Health.LastError, AccessErrorAt: in.Health.AccessErrorAt, SilentAfter: in.SilentAfter, CredentialWarning: in.CredentialWarning}
	if in.Credential != nil {
		h.CredentialExpiresAt = in.Credential.ExpiresAt
	}
	h.PushAccessRefused = in.Health.Push != nil && in.Health.Push.AccessRefused()
	return h
}

// reevaluate commits the instance's current Connector Health, emitting
// connector.health_changed when the state changes. The caller holds the
// journal lock and the row lock.
func reevaluate(ctx context.Context, tx pgx.Tx, org, id string) (connectors.Instance, error) {
	in, err := readConnector(ctx, tx, org, id, false)
	if err != nil {
		return in, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		return in, err
	}
	state := connectors.Evaluate(healthInput(in), now)
	if state == in.Health.State {
		_, err = tx.Exec(ctx, "UPDATE connector_instances SET health_evaluated_at=$3 WHERE organization=$1 AND id=$2", org, id, now)
		in.Health.EvaluatedAt = now
		return in, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, "UPDATE connector_instances SET health_state=$3,health_evaluated_at=$4,health_revision=health_revision+1 WHERE organization=$1 AND id=$2 RETURNING health_revision", org, id, state, now).Scan(&revision); err != nil {
		return in, err
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: in.CorpusID, Kind: "connector.health_changed", Resource: "connector", ResourceID: id, MutationID: content.StableID("health", id, strconv.FormatInt(revision, 10), state)}); err != nil {
		return in, err
	}
	in.Health.State, in.Health.EvaluatedAt = state, now
	return in, nil
}

// CreateConnector inserts an instance with its optional first credential, or
// replays an existing one created under the same key and request.
func (s ConnectorStore) CreateConnector(ctx context.Context, n connectors.NewInstance) (connectors.Instance, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return connectors.Instance{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, n.Organization); err != nil {
		return connectors.Instance{}, err
	}
	var existing string
	var digest []byte
	err = tx.QueryRow(ctx, "SELECT id,request_digest FROM connector_instances WHERE organization=$1 AND request_key=$2", n.Organization, n.RequestKey).Scan(&existing, &digest)
	if err == nil {
		if !bytes.Equal(digest, n.RequestDigest) {
			return connectors.Instance{}, connectors.ErrConflict
		}
		return readConnector(ctx, tx, n.Organization, existing, false)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return connectors.Instance{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$2)", n.Organization, n.CorpusID).Scan(&exists); err != nil {
		return connectors.Instance{}, err
	}
	if !exists {
		return connectors.Instance{}, corpus.ErrNotFound
	}
	var inUse bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM connector_instances WHERE organization=$1 AND corpus_id=$2 AND source_namespace=$3 AND enabled)", n.Organization, n.CorpusID, n.Namespace).Scan(&inUse); err != nil {
		return connectors.Instance{}, err
	}
	if inUse {
		return connectors.Instance{}, connectors.ErrNamespaceInUse
	}
	_, err = tx.Exec(ctx, `INSERT INTO connector_instances(organization,id,corpus_id,source_namespace,kind,config,interval_seconds,silent_after_seconds,credential_warning_seconds,request_key,request_digest,health_state,push_policy)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'active',$12)`, n.Organization, n.ID, n.CorpusID, n.Namespace, n.Kind, []byte(n.Config), int64(n.Interval/time.Second), int64(n.SilentAfter/time.Second), int64(n.CredentialWarning/time.Second), n.RequestKey, n.RequestDigest, n.PushPolicy)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "connector_instances_enabled_namespace" {
		return connectors.Instance{}, connectors.ErrNamespaceInUse
	}
	if err != nil {
		return connectors.Instance{}, err
	}
	if n.Credential != nil {
		if err = insertCredential(ctx, tx, n.Organization, n.ID, 1, *n.Credential, "", nil); err != nil {
			return connectors.Instance{}, err
		}
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: n.Organization, CorpusID: n.CorpusID, Kind: "connector.created", Resource: "connector", ResourceID: n.ID}); err != nil {
		return connectors.Instance{}, err
	}
	// The initial state is part of creation, not a transition: store it without an event.
	in, err := readConnector(ctx, tx, n.Organization, n.ID, false)
	if err != nil {
		return in, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		return in, err
	}
	in.Health.State = connectors.Evaluate(healthInput(in), now)
	if _, err = tx.Exec(ctx, "UPDATE connector_instances SET health_state=$3,health_evaluated_at=$4 WHERE organization=$1 AND id=$2", n.Organization, n.ID, in.Health.State, now); err != nil {
		return in, err
	}
	in.Health.EvaluatedAt = now
	return in, tx.Commit(ctx)
}

func insertCredential(ctx context.Context, tx pgx.Tx, org, id string, version int, sealed connectors.Sealed, key string, digest []byte) error {
	var requestKey any
	if key != "" {
		requestKey = key
	}
	_, err := tx.Exec(ctx, `INSERT INTO connector_credentials(organization,connector_id,version,key_id,nonce,ciphertext,expires_at,request_key,request_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, org, id, version, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, sealed.ExpiresAt, requestKey, digest)
	return err
}

// ReadConnector reads one instance of an Organization.
func (s ConnectorStore) ReadConnector(ctx context.Context, org, id string) (connectors.Instance, error) {
	return readConnector(ctx, database(ctx, s.Pool), org, id, false)
}

// ListConnectors pages authorized instances in identifier order.
func (s ConnectorStore) ListConnectors(ctx context.Context, scope corpus.Scope, corpusID, after string, limit int) ([]connectors.Instance, error) {
	rows, err := database(ctx, s.Pool).Query(ctx, "SELECT "+connectorColumns+connectorFrom+` WHERE c.organization=$1 AND c.id>$2 AND ($3 OR c.corpus_id=ANY($4)) AND ($5='' OR c.corpus_id=$5) ORDER BY c.id LIMIT $6`, scope.Organization, after, scope.AllCorpora(), scope.Corpora, corpusID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []connectors.Instance{}
	for rows.Next() {
		in, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, in)
	}
	return result, rows.Err()
}

// DisableConnector disables an instance once; repeats return it unchanged.
func (s ConnectorStore) DisableConnector(ctx context.Context, org, id string) (connectors.Instance, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return connectors.Instance{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return connectors.Instance{}, err
	}
	in, err := readConnector(ctx, tx, org, id, true)
	if err != nil || !in.Enabled {
		return in, err
	}
	if _, err = tx.Exec(ctx, "UPDATE connector_instances SET enabled=false,disabled_at=now(),lease_until=NULL WHERE organization=$1 AND id=$2", org, id); err != nil {
		return in, err
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: in.CorpusID, Kind: "connector.disabled", Resource: "connector", ResourceID: id}); err != nil {
		return in, err
	}
	if in, err = reevaluate(ctx, tx, org, id); err != nil {
		return in, err
	}
	return in, tx.Commit(ctx)
}

// ChangeSchedule sets the polling interval of an enabled instance. An
// unchanged value commits nothing. A shorter interval pulls the next run in; a
// longer one applies after the run already scheduled.
func (s ConnectorStore) ChangeSchedule(ctx context.Context, org, id string, interval time.Duration) (connectors.Instance, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return connectors.Instance{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return connectors.Instance{}, err
	}
	in, err := readConnector(ctx, tx, org, id, true)
	if err != nil {
		return in, err
	}
	if !in.Enabled {
		return connectors.Instance{}, connectors.ErrDisabled
	}
	if in.Interval == interval {
		return in, nil
	}
	seconds := int64(interval / time.Second)
	if _, err = tx.Exec(ctx, "UPDATE connector_instances SET interval_seconds=$3,next_run_at=LEAST(next_run_at,now()+make_interval(secs => $4)) WHERE organization=$1 AND id=$2", org, id, seconds, float64(seconds)); err != nil {
		return in, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return in, err
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: in.CorpusID, Kind: "connector.schedule_changed", Resource: "connector", ResourceID: id, MutationID: content.StableID("schedule", id, hex.EncodeToString(nonce[:]))}); err != nil {
		return in, err
	}
	in.Interval = interval
	return in, tx.Commit(ctx)
}

// RequestRun pulls the next run of an enabled instance in, bounded by the
// floor after the last run and by the source's Retry-After, in one statement
// so a concurrent FinishRun or disable is seen whole. Repeating it is a no-op.
func (s ConnectorStore) RequestRun(ctx context.Context, org, id string, floor time.Duration) (time.Time, error) {
	var archived bool
	if err := database(ctx, s.Pool).QueryRow(ctx, `SELECT cp.archived FROM connector_instances ci JOIN corpora cp ON (cp.organization,cp.id)=(ci.organization,ci.corpus_id) WHERE ci.organization=$1 AND ci.id=$2`, org, id).Scan(&archived); err != nil {
		return time.Time{}, notFound(err)
	}
	if archived {
		return time.Time{}, corpus.ErrArchived
	}
	var enabled bool
	var at time.Time
	err := database(ctx, s.Pool).QueryRow(ctx, `UPDATE connector_instances SET next_run_at=CASE WHEN enabled THEN LEAST(next_run_at,GREATEST(now(),
  COALESCE(last_run_at+make_interval(secs => $3::double precision),now()),COALESCE(retry_until,now()))) ELSE next_run_at END
WHERE organization=$1 AND id=$2 RETURNING enabled,GREATEST(next_run_at,now())`, org, id, floor.Seconds()).Scan(&enabled, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return at, corpus.ErrNotFound
	}
	if err == nil && !enabled {
		err = connectors.ErrDisabled
	}
	return at, err
}

// ReplaceCredential deposits the next credential version, or replays one
// deposited under the same key and request.
func (s ConnectorStore) ReplaceCredential(ctx context.Context, org, id string, d connectors.CredentialDeposit) (connectors.Instance, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return connectors.Instance{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return connectors.Instance{}, err
	}
	in, err := readConnector(ctx, tx, org, id, true)
	if err != nil {
		return in, err
	}
	var digest []byte
	err = tx.QueryRow(ctx, "SELECT request_digest FROM connector_credentials WHERE organization=$1 AND connector_id=$2 AND request_key=$3", org, id, d.RequestKey).Scan(&digest)
	if err == nil {
		if !bytes.Equal(digest, d.RequestDigest) {
			return connectors.Instance{}, connectors.ErrConflict
		}
		return in, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return in, err
	}
	if !in.Enabled {
		return connectors.Instance{}, connectors.ErrDisabled
	}
	version := 1
	if in.Credential != nil {
		version = in.Credential.Version + 1
	}
	if err = insertCredential(ctx, tx, org, id, version, d.Sealed, d.RequestKey, d.RequestDigest); err != nil {
		return in, err
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: in.CorpusID, Kind: "connector.credential_replaced", Resource: "connector", ResourceID: id, MutationID: content.StableID("credential", id, d.RequestKey)}); err != nil {
		return in, err
	}
	if in, err = reevaluate(ctx, tx, org, id); err != nil {
		return in, err
	}
	return in, tx.Commit(ctx)
}

// ClaimConnectorRuns leases enabled instances whose next run is due. A lease
// hides an instance from other dispatchers until it expires or the run ends;
// the run sequence stays stable so a re-dispatch targets the same run.
func (s ConnectorStore) ClaimConnectorRuns(ctx context.Context, lease time.Duration, limit int) ([]connectors.ConnectorRun, error) {
	rows, err := database(ctx, s.Pool).Query(ctx, `UPDATE connector_instances c SET lease_until=now()+make_interval(secs => $1::double precision)
FROM (SELECT organization,id FROM connector_instances ci WHERE enabled AND NOT EXISTS(SELECT 1 FROM corpora cp WHERE cp.organization=ci.organization AND cp.id=ci.corpus_id AND cp.archived) AND next_run_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_run_at LIMIT $2 FOR UPDATE SKIP LOCKED) d
WHERE c.organization=d.organization AND c.id=d.id RETURNING c.organization,c.id,c.run_sequence`, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []connectors.ConnectorRun
	for rows.Next() {
		var r connectors.ConnectorRun
		if err = rows.Scan(&r.Organization, &r.ConnectorID, &r.Run); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// ReleaseConnectorRun drops a lease whose run could not be dispatched.
func (s ConnectorStore) ReleaseConnectorRun(ctx context.Context, r connectors.ConnectorRun) error {
	_, err := database(ctx, s.Pool).Exec(ctx, "UPDATE connector_instances SET lease_until=NULL WHERE organization=$1 AND id=$2 AND run_sequence=$3", r.Organization, r.ConnectorID, r.Run)
	return err
}

// BeginPoll admits one external attempt while holding the corpus row shared.
// Archive cannot commit until admitted attempts release; later attempts observe
// the archive even when their run target was loaded before the transition.
func (s ConnectorStore) BeginPoll(ctx context.Context, org, id string, run int64) (func(), bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT ci.enabled AND ci.run_sequence=$3 AND NOT cp.archived
 FROM connector_instances ci JOIN corpora cp ON (cp.organization,cp.id)=(ci.organization,ci.corpus_id)
 WHERE ci.organization=$1 AND ci.id=$2 FOR SHARE OF cp`, org, id, run).Scan(&active)
	if err != nil || !active {
		release()
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		return nil, false, err
	}
	return release, true, nil
}

// LoadRun reads an instance, its checkpoint and its current sealed credential.
func (s ConnectorStore) LoadRun(ctx context.Context, org, id string) (connectors.Target, error) {
	in, err := readConnector(ctx, database(ctx, s.Pool), org, id, false)
	if err != nil {
		return connectors.Target{}, err
	}
	return s.target(ctx, in)
}

// LoadDelivery reads an instance by id alone, for the public webhook route:
// instance ids are random, so an id names at most one instance, and an
// ambiguous id is treated as unknown.
func (s ConnectorStore) LoadDelivery(ctx context.Context, id string) (connectors.Target, error) {
	rows, err := database(ctx, s.Pool).Query(ctx, "SELECT "+connectorColumns+connectorFrom+" WHERE c.id=$1 LIMIT 2", id)
	if err != nil {
		return connectors.Target{}, err
	}
	var found []connectors.Instance
	for rows.Next() {
		in, err := scanConnector(rows)
		if err != nil {
			rows.Close()
			return connectors.Target{}, err
		}
		found = append(found, in)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return connectors.Target{}, err
	}
	if len(found) != 1 {
		return connectors.Target{}, corpus.ErrNotFound
	}
	return s.target(ctx, found[0])
}

func (s ConnectorStore) target(ctx context.Context, in connectors.Instance) (connectors.Target, error) {
	org, id := in.Organization, in.ID
	var archived bool
	if err := database(ctx, s.Pool).QueryRow(ctx, `SELECT archived FROM corpora WHERE organization=$1 AND id=$2`, org, in.CorpusID).Scan(&archived); err != nil {
		return connectors.Target{}, notFound(err)
	}
	if archived {
		in.Enabled = false
	}
	t := connectors.Target{Instance: in}
	var err error
	if in.Health.Usage != nil {
		t.ReadsToday = in.Health.Usage.ItemsRead
	}
	if err = database(ctx, s.Pool).QueryRow(ctx, "SELECT run_sequence,checkpoint FROM connector_instances WHERE organization=$1 AND id=$2", org, id).Scan(&t.RunSequence, &t.Checkpoint); err != nil {
		return t, err
	}
	if in.Credential != nil {
		sealed := connectors.Sealed{ExpiresAt: in.Credential.ExpiresAt}
		if err = database(ctx, s.Pool).QueryRow(ctx, "SELECT key_id,nonce,ciphertext FROM connector_credentials WHERE organization=$1 AND connector_id=$2 AND version=$3", org, id, in.Credential.Version).Scan(&sealed.KeyID, &sealed.Nonce, &sealed.Ciphertext); err != nil {
			return t, err
		}
		t.Sealed = &sealed
	}
	return t, nil
}

// CommitCheckpoint advances the Acquisition Checkpoint of a live run, adds
// the page's reads to the current UTC day's usage (rolling the counters over
// on a new day) and replaces the kind's diagnostics when the page has any.
func (s ConnectorStore) CommitCheckpoint(ctx context.Context, org, id string, run int64, p connectors.Progress) (bool, error) {
	var diagnostics any
	if len(p.Diagnostics) > 0 {
		diagnostics = []byte(p.Diagnostics)
	}
	// A push report replaces the previous one (setup_at moves only when it
	// changes); missed deliveries record a delivery error.
	var pushState, pushClass, pushCode, pushInterval any
	if p.Push != nil {
		pushState = p.Push.State
		if p.Push.Class != "" {
			pushClass = string(p.Push.Class)
		}
		if p.Push.Code != "" {
			pushCode = p.Push.Code
		}
		if p.Push.PollInterval > 0 {
			pushInterval = int64(p.Push.PollInterval / time.Second)
		}
	}
	tag, err := database(ctx, s.Pool).Exec(ctx, `UPDATE connector_instances SET checkpoint=$4, last_item_at=CASE WHEN $5 THEN now() ELSE last_item_at END,
 usage_previous_items=CASE WHEN usage_day=`+utcToday+` THEN usage_previous_items WHEN usage_day=`+utcToday+`-1 THEN usage_items ELSE 0 END,
 usage_items=CASE WHEN usage_day=`+utcToday+` THEN usage_items+$6 ELSE $6 END,
 usage_day=CASE WHEN usage_day IS NULL AND $6=0 THEN NULL ELSE `+utcToday+` END,
 diagnostics=COALESCE($7::jsonb,diagnostics),
 push_setup_at=CASE WHEN $8::text IS NULL OR ($8 IS NOT DISTINCT FROM push_state AND $9::text IS NOT DISTINCT FROM push_setup_class AND $10::text IS NOT DISTINCT FROM push_setup_code) THEN push_setup_at ELSE now() END,
 push_state=COALESCE($8,push_state),
 push_setup_class=CASE WHEN $8::text IS NULL THEN push_setup_class ELSE $9 END,
 push_setup_code=CASE WHEN $8::text IS NULL THEN push_setup_code ELSE $10 END,
 push_poll_interval_seconds=CASE WHEN $8::text IS NULL THEN push_poll_interval_seconds ELSE $11::integer END,
 push_error_class=CASE WHEN $12 THEN 'transient' ELSE push_error_class END,
 push_error_code=CASE WHEN $12 THEN '`+connectors.CodeMissedDeliveries+`' ELSE push_error_code END,
 push_error_at=CASE WHEN $12 THEN now() ELSE push_error_at END
WHERE organization=$1 AND id=$2 AND run_sequence=$3 AND enabled AND NOT EXISTS(SELECT 1 FROM corpora cp WHERE cp.organization=connector_instances.organization AND cp.id=connector_instances.corpus_id AND cp.archived)`, org, id, run, []byte(p.Checkpoint), p.Items, p.Reads, diagnostics, pushState, pushClass, pushCode, pushInterval, p.Missed)
	return tag.RowsAffected() == 1, err
}

// RecordDelivery commits a relayed delivery's outcome: an accepted one moves
// the last delivery and usage, and clears the delivery error (missed
// deliveries only when it carried items); a failure records it and brings a
// pull run relaxed by healthy push back to the instance's interval. Connector
// Health is re-evaluated in the same transaction.
func (s ConnectorStore) RecordDelivery(ctx context.Context, org, id string, o connectors.DeliveryOutcome) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	var enabled bool
	if err = tx.QueryRow(ctx, "SELECT enabled FROM connector_instances WHERE organization=$1 AND id=$2 FOR UPDATE", org, id).Scan(&enabled); err != nil || !enabled {
		return err
	}
	var class, code any
	if o.Failure != nil {
		class, code = string(o.Failure.Class), o.Failure.Code
	}
	_, err = tx.Exec(ctx, `UPDATE connector_instances SET
 next_run_at=CASE WHEN $5::text IS NOT NULL THEN LEAST(next_run_at,now()+make_interval(secs => interval_seconds::double precision)) ELSE next_run_at END,
 push_error_class=CASE WHEN $5::text IS NOT NULL THEN $5 WHEN $3 AND (push_error_code IS DISTINCT FROM '`+connectors.CodeMissedDeliveries+`' OR $4) THEN NULL ELSE push_error_class END,
 push_error_code=CASE WHEN $5::text IS NOT NULL THEN $6 WHEN $3 AND (push_error_code IS DISTINCT FROM '`+connectors.CodeMissedDeliveries+`' OR $4) THEN NULL ELSE push_error_code END,
 push_error_at=CASE WHEN $5::text IS NOT NULL THEN now() WHEN $3 AND (push_error_code IS DISTINCT FROM '`+connectors.CodeMissedDeliveries+`' OR $4) THEN NULL ELSE push_error_at END,
 push_last_delivery_at=CASE WHEN $3 THEN now() ELSE push_last_delivery_at END,
 last_item_at=CASE WHEN $7 THEN now() ELSE last_item_at END,
 usage_previous_items=CASE WHEN $8=0 OR usage_day=`+utcToday+` THEN usage_previous_items WHEN usage_day=`+utcToday+`-1 THEN usage_items ELSE 0 END,
 usage_items=CASE WHEN $8=0 THEN usage_items WHEN usage_day=`+utcToday+` THEN usage_items+$8 ELSE $8 END,
 usage_day=CASE WHEN $8=0 THEN usage_day ELSE `+utcToday+` END
WHERE organization=$1 AND id=$2`, org, id, o.Accepted, o.Carried, class, code, o.Fresh, o.Reads)
	if err != nil {
		return err
	}
	if _, err = reevaluate(ctx, tx, org, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinishRun ends a live run: it records its outcome, schedules the next run
// one interval later (or after the failure's RetryAfter when longer),
// releases the lease and commits re-evaluated health.
func (s ConnectorStore) FinishRun(ctx context.Context, org, id string, run int64, failure *connectors.RunError) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Order this lock before the journal and connector locks. The archive
	// transition waits until this live finish transaction has committed.
	var archived bool
	if err = tx.QueryRow(ctx, `SELECT cp.archived FROM connector_instances ci JOIN corpora cp ON (cp.organization,cp.id)=(ci.organization,ci.corpus_id) WHERE ci.organization=$1 AND ci.id=$2 FOR SHARE OF cp`, org, id).Scan(&archived); err != nil || archived {
		return err
	}
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	var current int64
	var enabled bool
	err = tx.QueryRow(ctx, "SELECT run_sequence,enabled FROM connector_instances WHERE organization=$1 AND id=$2 FOR UPDATE", org, id).Scan(&current, &enabled)
	if err != nil || current != run || !enabled {
		return err
	}
	// A successful poll (including one that only reported a rejected item)
	// resolves a previous access refusal; an access refusal records one; other
	// failures only update the last error.
	success := failure == nil || failure.Completed
	var code, class any
	retry := 0.0
	if failure != nil && !failure.Skipped {
		code, class, retry = failure.Code, string(failure.Class), failure.RetryAfter.Seconds()
	}
	// Healthy push relaxes pull to the kind's reported poll interval.
	_, err = tx.Exec(ctx, `UPDATE connector_instances SET run_sequence=run_sequence+1,lease_until=NULL,next_run_at=now()+make_interval(secs => GREATEST(interval_seconds::double precision,$6::double precision,
  CASE WHEN push_state='active' AND push_error_code IS NULL THEN COALESCE(push_poll_interval_seconds,0) ELSE 0 END::double precision)),
 last_success_at=CASE WHEN $3 THEN now() ELSE last_success_at END,last_run_at=now(),
 retry_until=CASE WHEN $6::double precision>0 THEN now()+make_interval(secs => $6::double precision) END,
 access_error_at=CASE WHEN $3 THEN NULL WHEN $5='access' THEN now() ELSE access_error_at END,
 last_error_code=COALESCE($4,last_error_code),last_error_class=COALESCE($5,last_error_class),last_error_at=CASE WHEN $4::text IS NULL THEN last_error_at ELSE now() END
WHERE organization=$1 AND id=$2`, org, id, success, code, class, retry)
	if err != nil {
		return err
	}
	if _, err = reevaluate(ctx, tx, org, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
