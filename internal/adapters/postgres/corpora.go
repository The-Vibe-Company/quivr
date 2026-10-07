package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

func (s Store) Create(ctx context.Context, org string, input corpus.CreateInput) (corpus.Corpus, bool, error) {
	canonical, err := json.Marshal(input)
	if err != nil {
		return corpus.Corpus{}, false, err
	}
	retrieval, err := json.Marshal(input.Resolved)
	if err != nil {
		return corpus.Corpus{}, false, err
	}
	var idbytes [16]byte
	if _, err = rand.Read(idbytes[:]); err != nil {
		return corpus.Corpus{}, false, err
	}
	c := corpus.Corpus{}
	var stored, config []byte
	err = database(ctx, s.Pool).QueryRow(ctx, `INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(organization,request_key) DO UPDATE SET request_key=EXCLUDED.request_key RETURNING id,name,retrieval,canonical_request,archived`, org, "corpus_"+hex.EncodeToString(idbytes[:]), input.Key, canonical, input.Name, retrieval).Scan(&c.ID, &c.Name, &config, &stored, &c.Archived)
	if err != nil {
		return c, false, err
	}
	err = json.Unmarshal(config, &c.Retrieval)
	return c, !bytes.Equal(canonical, stored), err
}

// effectiveRetrievalSQL is Corpus c's effective retrieval configuration: the
// pin of its routed generation, or its creation configuration when that
// generation pins none. It changes only when routing switches.
var effectiveRetrievalSQL = `COALESCE((SELECT g.retrieval FROM projection_generations g WHERE g.id=` + routedGenerationSQL("c.organization", "c.id") + `),c.retrieval)`

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
