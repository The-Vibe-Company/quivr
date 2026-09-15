package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

func (s Store) Create(ctx context.Context, org string, input corpus.CreateInput) (corpus.Corpus, bool, error) {
	canonical, err := json.Marshal(input)
	if err != nil {
		return corpus.Corpus{}, false, err
	}
	retrieval, err := json.Marshal(input.Retrieval)
	if err != nil {
		return corpus.Corpus{}, false, err
	}
	var idbytes [16]byte
	if _, err = rand.Read(idbytes[:]); err != nil {
		return corpus.Corpus{}, false, err
	}
	c := corpus.Corpus{}
	var stored, config []byte
	err = s.Pool.QueryRow(ctx, `INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(organization,request_key) DO UPDATE SET request_key=EXCLUDED.request_key RETURNING id,name,retrieval,canonical_request`, org, "corpus_"+hex.EncodeToString(idbytes[:]), input.Key, canonical, input.Name, retrieval).Scan(&c.ID, &c.Name, &config, &stored)
	if err != nil {
		return c, false, err
	}
	err = json.Unmarshal(config, &c.Retrieval)
	return c, !bytes.Equal(canonical, stored), err
}
func (s Store) Read(ctx context.Context, org, id string) (corpus.Corpus, error) {
	c := corpus.Corpus{}
	var data []byte
	err := s.Pool.QueryRow(ctx, "SELECT id,name,retrieval FROM corpora WHERE organization=$1 AND id=$2", org, id).Scan(&c.ID, &c.Name, &data)
	if err == nil {
		err = json.Unmarshal(data, &c.Retrieval)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = corpus.ErrNotFound
	}
	return c, err
}
func (s Store) List(ctx context.Context, scope corpus.Scope, after string, limit int) ([]corpus.Corpus, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,retrieval FROM corpora WHERE organization=$1 AND id>$2 AND ($3 OR id=ANY($4)) ORDER BY id LIMIT $5`, scope.Organization, after, scope.AllCorpora(), scope.Corpora, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]corpus.Corpus, 0)
	for rows.Next() {
		var c corpus.Corpus
		var data []byte
		if err = rows.Scan(&c.ID, &c.Name, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &c.Retrieval); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(642001)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY)"); err != nil {
		return err
	}
	files, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, f := range files {
		var applied bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)", f.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrations.Files.ReadFile(f.Name())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s failed: %w", f.Name(), err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", f.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
