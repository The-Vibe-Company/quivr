package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

var _ content.FacetReader = RecordStore{}

// CountFacets aggregates current eligible Record Versions in one statement,
// so every field sees the same snapshot. Arrays count documents, not members.
func (s RecordStore) CountFacets(ctx context.Context, org string, q content.FacetQuery) ([]content.Facet, error) {
	counted, err := facetRows(ctx, database(ctx, s.Pool), org, q, "")
	if err != nil {
		return nil, err
	}
	return decodeFacets(q.Fields, counted)
}

// rawBucket keeps PostgreSQL's JSON text of a value: ties order by it.
type rawBucket struct {
	Value json.RawMessage `json:"v"`
	Count int64           `json:"n"`
}

func decodeFacets(fields []content.FacetField, counted [][]rawBucket) ([]content.Facet, error) {
	out := make([]content.Facet, len(fields))
	for i, f := range fields {
		out[i] = content.Facet{Field: f.Field, Buckets: []content.FacetBucket{}}
		for _, raw := range counted[i] {
			b := content.FacetBucket{Count: raw.Count}
			if err := json.Unmarshal(raw.Value, &b.Value); err != nil {
				return nil, err
			}
			out[i].Buckets = append(out[i].Buckets, b)
		}
	}
	return out, nil
}

// facetRows counts each field's buckets; below, when set, keeps only Records
// whose ID sorts under it: a uniform sample, since Record IDs are hashes.
func facetRows(ctx context.Context, db querier, org string, q content.FacetQuery, below string) ([][]rawBucket, error) {
	out := make([][]rawBucket, len(q.Fields))
	if len(q.Records.FilterRoutes) == 0 {
		return out, nil
	}
	args := []any{org, q.Records.CorpusIDs}
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	rangeQuery := q.Records
	rangeQuery.Metadata = nil
	where := catalogRange(rangeQuery, &args)
	where += " AND (" + metadataRouteConditions(q.Records, &args) + ")"
	if len(q.SourceNamespaces) > 0 {
		where += " AND records.namespace=ANY(" + bind(q.SourceNamespaces) + "::text[])"
	}
	if below != "" {
		where += ` AND records.id COLLATE "C" >= ` + bind(recordIDPrefix) + ` AND records.id COLLATE "C" < ` + bind(below)
	}
	where += " AND " + strings.ReplaceAll(eligibleVersionSQL, "r.", "records.")
	columns, branches := []string{}, []string{}
	groups := []string{}
	multi := len(q.Fields) > 1
	dateFields := []int{}
	for i, f := range q.Fields {
		column := fmt.Sprintf("f%d", i)
		columns = append(columns, "pm.data -> "+bind(f.Field)+"::text AS "+column)
		groups = append(groups, fmt.Sprint(i+1))
		present := "m." + column + " IS NOT NULL AND m." + column + " <> 'null'::jsonb"
		value, from, count := "m."+column, "matched m", "count(*)"
		if multi {
			count = "sum(m.documents)::bigint"
		}
		if f.Type == "string_array" || f.Type == "datetime" {
			// Count equal raw values first. Repeated arrays need deduplication
			// only once, and repeated timestamps need calendar parsing only once.
			// Summing each group's document count preserves exact bucket counts.
			from = "(SELECT m." + column + " AS raw_value," + count + " AS documents FROM matched m WHERE " + present + " GROUP BY 1) grouped"
			value, count, present = "grouped.raw_value", "sum(grouped.documents)::bigint", "true"
		}
		if f.Type == "string_array" {
			from += " CROSS JOIN LATERAL (SELECT DISTINCT value FROM jsonb_array_elements(grouped.raw_value)) member"
			value = "member.value"
		}
		if f.Type == "datetime" {
			value = "to_jsonb(to_char(date_trunc(" + bind(f.Interval) + "::text,(" + value + " #>> '{}')::timestamptz AT TIME ZONE 'UTC'),'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'))"
			dateFields = append(dateFields, i)
		}
		branches = append(branches, "(SELECT "+bind(i)+"::int AS idx,"+value+" AS value,"+count+" AS count FROM "+from+
			" WHERE "+present+" GROUP BY 2 ORDER BY count DESC,"+value+"::text COLLATE \"C\" LIMIT "+bind(f.Limit)+")")
	}
	// A single field can be aggregated directly in a parallel scan. Multiple
	// fields share just their projected values, avoiding repeated storage joins
	// and carrying unrelated JSON through PostgreSQL's materialized result.
	materialized := "MATERIALIZED"
	groupBy := ""
	if !multi {
		materialized = "NOT MATERIALIZED"
	} else {
		// Collapse equal facet tuples before materializing: sixteen fields
		// should share one scan, rather than reread one wide tuple per document
		// for every field. Weighted bucket sums preserve document counts.
		columns = append(columns, "count(*) AS documents")
		groupBy = " GROUP BY " + strings.Join(groups, ",")
	}
	sql := "WITH matched AS " + materialized + " (SELECT " + strings.Join(columns, ",") + ` FROM records
 JOIN record_versions v ON v.organization=records.organization AND v.id=records.current_version_id
 JOIN projection_metadata pm ON pm.organization=records.organization AND pm.version_id=v.id
	WHERE ` + where + groupBy + ") SELECT idx,value,count FROM (" + strings.Join(branches, " UNION ALL ") + ") counted ORDER BY idx,CASE WHEN idx=ANY(" + bind(dateFields) + `::int[]) THEN value::text END COLLATE "C",count DESC,value::text COLLATE "C"`

	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i int
		var raw []byte
		b := rawBucket{}
		if err = rows.Scan(&i, &raw, &b.Count); err != nil {
			return nil, err
		}
		b.Value = raw
		out[i] = append(out[i], b)
	}
	return out, rows.Err()
}
