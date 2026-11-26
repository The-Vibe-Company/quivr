package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CorpusProjection interface {
	EnsureGeneration(ctx context.Context, collection, org, corpusID, generationID string) error
	SetGenerationActive(ctx context.Context, collection, org, corpusID, generationID string, active bool) error
}

type Store struct {
	Pool             *pgxpool.Pool
	Projection       CorpusProjection
	TenantCollection string
}

func (s Store) Create(ctx context.Context, org string, input corpus.CreateInput) (corpus.Corpus, bool, error) {
	canonical, err := json.Marshal(input)
	if err != nil {
		return corpus.Corpus{}, false, err
	}
	retrieval, err := json.Marshal(input.Resolved)
	if err != nil {
		return corpus.Corpus{}, false, err
	}
	// A lost response after tenant creation can be retried with the same
	// tenant identity, even when the PostgreSQL transaction rolled back.
	id := content.StableID("corpus", org, input.Key)[:len("corpus_")+32]
	db := database(ctx, s.Pool)
	if s.TenantCollection != "" && s.Projection == nil {
		return corpus.Corpus{}, false, errors.New("tenant provisioning unavailable")
	}
	if s.TenantCollection == "" {
		c, conflict, _, err := createCorpus(ctx, db, org, id, input.Key, input.Name, canonical, retrieval)
		return c, conflict, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return corpus.Corpus{}, false, err
	}
	defer tx.Rollback(ctx)
	if err := lockProjectionRouting(ctx, tx); err != nil {
		return corpus.Corpus{}, false, err
	}
	c, conflict, created, err := createCorpus(ctx, tx, org, id, input.Key, input.Name, canonical, retrieval)
	if err != nil || conflict {
		return c, conflict, err
	}
	if created {
		gen := content.StableID("generation", s.TenantCollection, org, id)
		if err := pinCorpusGeneration(ctx, tx, s.TenantCollection, org, id, gen); err != nil {
			return c, false, err
		}
		if err := s.Projection.EnsureGeneration(ctx, s.TenantCollection, org, id, gen); err != nil {
			return c, false, err
		}
	}
	return c, false, tx.Commit(ctx)
}

// Pin only the Corpora created by the tenant-aware API. The shared default
// remains readable by previous binaries while the upgrade is rolling.
func pinCorpusGeneration(ctx context.Context, tx pgx.Tx, collection, org, id, generation string) error {
	tag, err := tx.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,active,space_id,organization,corpus_id,source_namespace_projected,spaces,spaces_projected,metadata_projected,item_keywords_projected,ingestion_routing,coverage_routed)
 SELECT $1,$2,d.profile_version,false,d.space_id,$3,$4,true,d.spaces,d.spaces_projected,true,true,d.ingestion_routing,d.coverage_routed
 FROM `+effectiveGenerationsSQL+` d WHERE d.active`, generation, collection, org, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("default projection generation missing")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO corpus_projection_routes(organization,corpus_id,generation_id) VALUES($1,$2,$3)`, org, id, generation); err != nil {
		return err
	}
	if err := markRoutingGeneration(ctx, tx, generation); err != nil {
		return err
	}
	return markRoutingCorpus(ctx, tx, org, id)
}

func corpusProjectionRoute(ctx context.Context, db querier, org, id string) (collection, generation string, err error) {
	err = db.QueryRow(ctx, `SELECT g.collection,g.id FROM `+effectiveGenerationsSQL+` g WHERE g.id=`+routedGenerationSQL("$1", "$2"), org, id).Scan(&collection, &generation)
	return
}

func createCorpus(ctx context.Context, db querier, org, id, key, name string, canonical, retrieval []byte) (c corpus.Corpus, conflict, created bool, err error) {
	var stored, config []byte
	err = db.QueryRow(ctx, `INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(organization,request_key) DO NOTHING RETURNING id,name,retrieval,canonical_request,archived`, org, id, key, canonical, name, retrieval).Scan(&c.ID, &c.Name, &config, &stored, &c.Archived)
	created = err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		err = db.QueryRow(ctx, `SELECT id,name,retrieval,canonical_request,archived FROM corpora WHERE organization=$1 AND request_key=$2 FOR UPDATE`, org, key).Scan(&c.ID, &c.Name, &config, &stored, &c.Archived)
	}
	if err != nil {
		return c, false, false, err
	}
	err = json.Unmarshal(config, &c.Retrieval)
	return c, !bytes.Equal(canonical, stored), created, err
}

// effectiveRetrievalSQL is Corpus c's effective retrieval configuration: the
// pin of its routed generation, or its creation configuration when that
// generation pins none. It changes only when routing switches.
var effectiveRetrievalSQL = `COALESCE((SELECT g.retrieval FROM ` + effectiveGenerationsSQL + ` g WHERE g.id=` + routedGenerationSQL("c.organization", "c.id") + `),c.retrieval)`

// retrievalFields decodes the fields of a stored retrieval configuration.
func retrievalFields(data []byte) ([]corpus.Field, error) {
	var cfg corpus.Retrieval
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg.Fields, nil
}

func (s Store) Read(ctx context.Context, org, id string) (corpus.Corpus, error) {
	return readCorpus(ctx, database(ctx, s.Pool), org, id)
}

func readCorpus(ctx context.Context, db querier, org, id string) (corpus.Corpus, error) {
	c := corpus.Corpus{}
	var data []byte
	err := db.QueryRow(ctx, `SELECT c.id,c.name,c.archived,`+effectiveRetrievalSQL+` FROM corpora c WHERE c.organization=$1 AND c.id=$2`, org, id).Scan(&c.ID, &c.Name, &c.Archived, &data)
	if err == nil {
		err = json.Unmarshal(data, &c.Retrieval)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = corpus.ErrNotFound
	}
	return c, err
}
func (s Store) List(ctx context.Context, scope corpus.Scope, after string, limit int) ([]corpus.Corpus, error) {
	return s.ListArchived(ctx, scope, after, limit, false)
}
func (s Store) ListArchived(ctx context.Context, scope corpus.Scope, after string, limit int, include bool) ([]corpus.Corpus, error) {
	rows, err := database(ctx, s.Pool).Query(ctx, `SELECT c.id,c.name,c.archived,`+effectiveRetrievalSQL+` FROM corpora c WHERE c.organization=$1 AND ($6 OR NOT c.archived) AND c.id>$2 AND ($3 OR c.id=ANY($4)) ORDER BY c.id LIMIT $5`, scope.Organization, after, scope.AllCorpora(), scope.Corpora, limit, include)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]corpus.Corpus, 0)
	for rows.Next() {
		var c corpus.Corpus
		var data []byte
		if err = rows.Scan(&c.ID, &c.Name, &c.Archived, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &c.Retrieval); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
