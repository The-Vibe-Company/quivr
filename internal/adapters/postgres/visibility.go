package postgres

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// recordVisibilitySQL is the canonical predicate. An absent source grant
// denies access. A nonempty record ACL takes precedence over its source.
func recordVisibilitySQL(record, principals string) string {
	return fmt.Sprintf(`(CASE WHEN cardinality(%[1]s.record_acl)>0
 THEN %[1]s.record_acl && %[2]s::text[]
 ELSE EXISTS (
   SELECT 1 FROM visibility_grants vg
   WHERE (vg.organization,vg.corpus_id,vg.source_namespace)=(%[1]s.organization,%[1]s.corpus_id,%[1]s.namespace)
     AND (vg.visibility='public' OR (vg.visibility='restricted' AND vg.allowed_principals && %[2]s::text[]))
 ) END)`, record, principals)
}

func visibilityPredicate(ctx context.Context, args *[]any, record string) string {
	principals, enforced := visibility.Principals(ctx)
	if !enforced {
		return ""
	}
	*args = append(*args, principals)
	return " AND " + recordVisibilitySQL(record, fmt.Sprintf("$%d", len(*args)))
}

// Add the first source and first ACL marker before advancing the Corpus
// revision. Existing sources and records leave the revision row untouched.
const recordVisibilityAdmissionSQL = `, source AS (
 INSERT INTO visibility_source_inventory(organization,corpus_id,source_namespace)
 SELECT organization,corpus_id,namespace FROM record ON CONFLICT DO NOTHING
 RETURNING organization,corpus_id
), acl_corpus AS (
 INSERT INTO visibility_acl_corpora(organization,corpus_id)
 SELECT organization,corpus_id FROM record WHERE cardinality(record_acl)>0
 AND (SELECT count(*) FROM source)>=0 ON CONFLICT DO NOTHING
 RETURNING organization,corpus_id
), visibility_revision AS (
 INSERT INTO visibility_revisions(organization,corpus_id,revision)
 SELECT organization,corpus_id,1 FROM (
  SELECT organization,corpus_id FROM source UNION SELECT organization,corpus_id FROM acl_corpus
 ) changed ON CONFLICT (organization,corpus_id)
 DO UPDATE SET revision=visibility_revisions.revision+1
)`

const sourceCacheTTL = 250 * time.Millisecond
const sourceCacheEntries = 256

type visibilityCorpus struct{ organization, id string }

type sourceGrant struct {
	namespace string
	grant     visibility.Grant
}

type sourceSnapshot struct {
	revision       int64
	expires        time.Time
	sources        []sourceGrant
	recordOverride bool
}

// sourceEntry is a cached source partition in the cache's recency list.
type sourceEntry struct {
	key      visibilityCorpus
	snapshot sourceSnapshot
}

// sourceFlight is one in-progress load of a source partition. Callers that
// find it under the current cache revision wait for it instead of loading.
// Its fields are written before done closes.
type sourceFlight struct {
	revision uint64
	done     chan struct{}
	snapshot sourceSnapshot
	err      error
	// current means no invalidation happened during the load: callers may
	// use the snapshot and it was cached.
	current bool
	// abandoned means the loading caller's context ended; waiters retry.
	abandoned bool
}

var errSourceLoadInterrupted = errors.New("visibility source load did not complete")

// VisibilityStore caches only the projection's source partition. Canonical
// reads always authorize against committed rows in their own SQL snapshot.
// Concurrent misses on one partition share one load; a full cache evicts its
// least recently used partitions, expired ones first from the cold end.
type VisibilityStore struct {
	Pool     *pgxpool.Pool
	mu       sync.Mutex
	cache    map[visibilityCorpus]*list.Element
	lru      list.List // of *sourceEntry, most recently used first
	flights  map[visibilityCorpus]*sourceFlight
	revision uint64
}

func (s *VisibilityStore) Grant(ctx context.Context, org, id, namespace string) (visibility.Grant, error) {
	var grant visibility.Grant
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT visibility,allowed_principals FROM visibility_grants
 WHERE organization=$1 AND corpus_id=$2 AND source_namespace=$3`, org, id, namespace).Scan(&grant.Visibility, &grant.AllowedPrincipals)
	return grant, notFound(err)
}

func (s *VisibilityStore) SetGrant(ctx context.Context, org, id, namespace string, grant visibility.Grant) error {
	tag, err := database(ctx, s.Pool).Exec(ctx, `WITH changed AS (INSERT INTO visibility_grants(organization,corpus_id,source_namespace,visibility,allowed_principals)
 SELECT $1,$2,$3,$4,coalesce($5::text[],'{}') WHERE EXISTS (SELECT 1 FROM corpora WHERE organization=$1 AND id=$2) ON CONFLICT (organization,corpus_id,source_namespace)
 DO UPDATE SET visibility=excluded.visibility,allowed_principals=excluded.allowed_principals
 RETURNING organization,corpus_id)
 INSERT INTO visibility_revisions(organization,corpus_id,revision)
 SELECT organization,corpus_id,1 FROM changed ON CONFLICT (organization,corpus_id)
 DO UPDATE SET revision=visibility_revisions.revision+1`, org, id, namespace, grant.Visibility, grant.AllowedPrincipals)
	if err == nil {
		if tag.RowsAffected() == 0 {
			return corpus.ErrNotFound
		}
		audit.AfterCommit(ctx, func(context.Context) { s.invalidate(visibilityCorpus{org, id}) })
	}
	return err
}

func (s *VisibilityStore) DeleteGrant(ctx context.Context, org, id, namespace string) error {
	var exists bool
	err := database(ctx, s.Pool).QueryRow(ctx, `WITH removed AS (
 DELETE FROM visibility_grants WHERE organization=$1 AND corpus_id=$2 AND source_namespace=$3 RETURNING organization,corpus_id
), rev AS (
 INSERT INTO visibility_revisions(organization,corpus_id,revision)
 SELECT organization,corpus_id,1 FROM removed ON CONFLICT (organization,corpus_id)
 DO UPDATE SET revision=visibility_revisions.revision+1
) SELECT EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$2)`, org, id, namespace).Scan(&exists)
	if err == nil {
		if !exists {
			return corpus.ErrNotFound
		}
		audit.AfterCommit(ctx, func(context.Context) { s.invalidate(visibilityCorpus{org, id}) })
	}
	return err
}

// invalidate advances the revision, so no load in flight is joined, cached
// or returned, and drops the partition's cached snapshot.
func (s *VisibilityStore) invalidate(key visibilityCorpus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	if e, ok := s.cache[key]; ok {
		s.lru.Remove(e)
		delete(s.cache, key)
	}
}

func (s *VisibilityStore) Sources(ctx context.Context, org, id string, principals []string) (visibility.Sources, error) {
	snapshot, err := s.snapshot(ctx, visibilityCorpus{org, id})
	if err != nil {
		return visibility.Sources{}, err
	}
	out := visibility.Sources{RecordOverride: snapshot.recordOverride, Revision: snapshot.revision}
	for _, source := range snapshot.sources {
		if source.grant.Allows(principals) {
			out.Allowed = append(out.Allowed, source.namespace)
		} else {
			out.Denied = append(out.Denied, source.namespace)
		}
	}
	return out, nil
}

func (s *VisibilityStore) snapshot(ctx context.Context, key visibilityCorpus) (sourceSnapshot, error) {
	return s.snapshotUsing(ctx, key, s.loadSources)
}

func (s *VisibilityStore) snapshotUsing(ctx context.Context, key visibilityCorpus, load func(context.Context, visibilityCorpus) (sourceSnapshot, error)) (sourceSnapshot, error) {
	for {
		start := time.Now()
		s.mu.Lock()
		if e, ok := s.cache[key]; ok {
			if entry := e.Value.(*sourceEntry); start.Before(entry.snapshot.expires) {
				s.lru.MoveToFront(e)
				s.mu.Unlock()
				return entry.snapshot, nil
			}
		}
		f := s.flights[key]
		lead := f == nil || f.revision != s.revision
		if lead {
			f = &sourceFlight{revision: s.revision, done: make(chan struct{})}
			if s.flights == nil {
				s.flights = make(map[visibilityCorpus]*sourceFlight)
			}
			s.flights[key] = f
		}
		s.mu.Unlock()
		if lead {
			s.fly(ctx, key, f, start, load)
		}
		select {
		case <-f.done:
		case <-ctx.Done():
			return sourceSnapshot{}, ctx.Err()
		}
		if f.current {
			return f.snapshot, nil
		}
		if f.err != nil && !f.abandoned {
			return sourceSnapshot{}, f.err
		}
		// An invalidation raced the load, or its caller gave up: load again.
		if err := ctx.Err(); err != nil {
			return sourceSnapshot{}, err
		}
	}
}

// fly runs flight f and publishes its result. A load that panics or exits
// abnormally fails the flight with errSourceLoadInterrupted for the loading
// caller and every waiter; only the panic's type and stack are logged, since
// its value may quote a query or an Organization.
func (s *VisibilityStore) fly(ctx context.Context, key visibilityCorpus, f *sourceFlight, start time.Time, load func(context.Context, visibilityCorpus) (sourceSnapshot, error)) {
	panicking := true
	defer func() {
		if panicking {
			r := recover()
			slog.ErrorContext(ctx, "visibility source load panicked", "panic_type", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
			f.snapshot, f.err, f.abandoned = sourceSnapshot{}, errSourceLoadInterrupted, false
		}
		s.mu.Lock()
		if s.flights[key] == f {
			delete(s.flights, key)
		}
		if f.err == nil && s.revision == f.revision {
			f.snapshot.expires = start.Add(sourceCacheTTL)
			f.current = true
			s.store(key, f.snapshot)
		}
		s.mu.Unlock()
		close(f.done)
	}()
	f.snapshot, f.err = load(ctx, key)
	f.abandoned = f.err != nil && ctx.Err() != nil
	panicking = false
}

// store caches a snapshot as the most recently used. It holds s.mu.
func (s *VisibilityStore) store(key visibilityCorpus, snapshot sourceSnapshot) {
	if e, ok := s.cache[key]; ok {
		e.Value.(*sourceEntry).snapshot = snapshot
		s.lru.MoveToFront(e)
		return
	}
	now := time.Now()
	for e := s.lru.Back(); e != nil; e = s.lru.Back() {
		cold := e.Value.(*sourceEntry)
		if len(s.cache) < sourceCacheEntries && now.Before(cold.snapshot.expires) {
			break
		}
		s.lru.Remove(e)
		delete(s.cache, cold.key)
	}
	if s.cache == nil {
		s.cache = make(map[visibilityCorpus]*list.Element)
	}
	s.cache[key] = s.lru.PushFront(&sourceEntry{key: key, snapshot: snapshot})
}

func (s *VisibilityStore) loadSources(ctx context.Context, key visibilityCorpus) (sourceSnapshot, error) {
	// Seek the next distinct namespace with the records identity index.
	rows, err := s.Pool.Query(ctx, `WITH RECURSIVE record_namespaces(namespace) AS (
 (SELECT namespace FROM records WHERE organization=$1 AND corpus_id=$2 ORDER BY namespace LIMIT 1)
 UNION ALL
 SELECT next.namespace FROM record_namespaces n CROSS JOIN LATERAL (
   SELECT namespace FROM records WHERE organization=$1 AND corpus_id=$2 AND namespace>n.namespace
   ORDER BY namespace LIMIT 1
 ) next
), namespaces AS (
 SELECT namespace FROM record_namespaces
 UNION SELECT source_namespace FROM visibility_source_inventory WHERE organization=$1 AND corpus_id=$2
), overrides AS MATERIALIZED (
 SELECT EXISTS (SELECT 1 FROM visibility_acl_corpora WHERE organization=$1 AND corpus_id=$2) AS present,
 coalesce((SELECT revision FROM visibility_revisions WHERE organization=$1 AND corpus_id=$2),0) AS revision
)
SELECT n.namespace,coalesce(v.visibility,''),coalesce(v.allowed_principals,'{}'),o.present,o.revision
FROM overrides o LEFT JOIN namespaces n ON true
LEFT JOIN visibility_grants v ON (v.organization,v.corpus_id,v.source_namespace)=($1,$2,n.namespace)
ORDER BY n.namespace`, key.organization, key.id)
	if err != nil {
		return sourceSnapshot{}, err
	}
	defer rows.Close()
	var out sourceSnapshot
	for rows.Next() {
		var ns *string
		var grant visibility.Grant
		if err = rows.Scan(&ns, &grant.Visibility, &grant.AllowedPrincipals, &out.recordOverride, &out.revision); err != nil {
			return sourceSnapshot{}, err
		}
		if ns != nil {
			out.sources = append(out.sources, sourceGrant{*ns, grant})
		}
	}
	return out, rows.Err()
}

func (s *VisibilityStore) Current(ctx context.Context, org string, revisions map[string]int64) (bool, error) {
	corpora := make([]string, 0, len(revisions))
	for id := range revisions {
		corpora = append(corpora, id)
	}
	rows, err := s.Pool.Query(ctx, `SELECT requested.id,coalesce(v.revision,0)
 FROM unnest($2::text[]) requested(id)
 LEFT JOIN visibility_revisions v ON (v.organization,v.corpus_id)=($1,requested.id)`, org, corpora)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	current := true
	for rows.Next() {
		var id string
		var revision int64
		if err := rows.Scan(&id, &revision); err != nil {
			return false, err
		}
		if revision != revisions[id] {
			current = false
			s.invalidate(visibilityCorpus{org, id})
		}
	}
	return current, rows.Err()
}

func (s *VisibilityStore) SourceVersions(ctx context.Context, org, id string, namespaces []string, limit int) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT current_version_id FROM records
 WHERE organization=$1 AND corpus_id=$2 AND namespace=ANY($3::text[]) AND current_version_id IS NOT NULL LIMIT $4`, org, id, namespaces, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
