package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

func (s RecordStore) SaveProjectionMetadata(ctx context.Context, org, versionID, generationID string, values map[string]any) error {
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Cancellation, version retirement and route cutover share this fence.
	// A publish delayed in the external projection cannot revive SQL metadata
	// for an abandoned generation after its purge has become terminal.
	// Prepared ingestion publishes before materialization, so its accepted
	// revision, rather than record_versions, supplies the reserved identity.
	var writable bool
	if err = readJournal(ctx, tx, org, `SELECT EXISTS(
 SELECT 1 FROM accepted_revisions a JOIN records r ON(r.organization,r.id)=(a.organization,a.record_id)
 WHERE a.organization=$1 AND a.version_id=$2 AND NOT `+recordGoneSQL+`
 AND (a.version_id=r.current_version_id OR a.version_id=r.desired_version_id)
 AND ($3=`+routedGenerationSQL("r.organization", "r.corpus_id")+` OR EXISTS(
  SELECT 1 FROM operations o WHERE o.organization=r.organization AND o.corpus_id=r.corpus_id
   AND o.target_generation_id=$3 AND o.state NOT IN ('succeeded','failed','canceled'))))`, []any{org, versionID, generationID}, &writable); err != nil {
		return err
	}
	if !writable {
		return tx.Commit(ctx)
	}
	// Each generation pins immutable mappings and Version input. Repeating a
	// publish repeats exactly these values, including when a plugin adds vectors.
	_, err = tx.Exec(ctx, `INSERT INTO projection_metadata(organization,version_id,generation_id,data) VALUES($1,$2,$3,$4) ON CONFLICT(organization,version_id,generation_id) DO UPDATE SET data=EXCLUDED.data`, org, versionID, generationID, raw)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// catalogMetadata applies predicates before ORDER BY/LIMIT and uses the same
// normalized typed values the search projection received.
func catalogMetadata(q content.RecordQuery, args *[]any) string {
	if len(q.Metadata) == 0 {
		return ""
	}
	conditions := metadataRouteConditions(q, args)
	return " AND EXISTS(SELECT 1 FROM projection_metadata pm WHERE pm.organization=records.organization AND pm.version_id=records.current_version_id AND (" + conditions + "))"
}

// metadataRouteConditions also serves aggregations that already join the
// pinned projection, avoiding a second metadata lookup for every document.
func metadataRouteConditions(q content.RecordQuery, args *[]any) string {
	bind := func(value any) string { *args = append(*args, value); return fmt.Sprintf("$%d", len(*args)) }
	branches := []string{}
	for _, route := range q.FilterRoutes {
		conditions := []string{"records.corpus_id=" + bind(route.CorpusID), "pm.generation_id=" + bind(route.GenerationID)}
		for _, f := range route.Filters {
			value := ""
			if f.Gte != "" || f.Lte != "" {
				key := bind(f.Field)
				value = "(pm.data -> " + key + "::text)"
			}
			if len(f.AnyOf) > 0 {
				equalities := []string{}
				for _, v := range f.AnyOf {
					var needle any = v
					if f.Type == "string_array" {
						needle = []any{v}
					}
					raw, _ := json.Marshal(map[string]any{f.Field: needle})
					equalities = append(equalities, "pm.data @> "+bind(raw)+"::jsonb")
				}
				conditions = append(conditions, "("+strings.Join(equalities, " OR ")+")")
			}
			if f.Gte != "" {
				conditions = append(conditions, "("+value+" #>> '{}')::timestamptz >= "+bind(f.Gte)+"::timestamptz")
			}
			if f.Lte != "" {
				conditions = append(conditions, "("+value+" #>> '{}')::timestamptz <= "+bind(f.Lte)+"::timestamptz")
			}
		}
		branches = append(branches, "("+strings.Join(conditions, " AND ")+")")
	}
	if len(branches) == 0 {
		return "false"
	}
	return strings.Join(branches, " OR ")
}
