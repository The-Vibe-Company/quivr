package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/observability"
	"github.com/jackc/pgx/v5"
)

var _ connectors.PushProtection = ConnectorStore{}

// ProtectPush reserves a durable key in a short transaction. Duplicate callers
// release their connection while waiting, so a busy key cannot starve ingestion.
// The pending lease outlives the request deadline by a minute; after a crash,
// stable item revisions still make a retried ingestion converge.
func (s ConnectorStore) ProtectPush(ctx context.Context, in connectors.PushAttempt, invoke func() (connectors.RelayAnswer, error)) (connectors.RelayAnswer, error) {
	owner := ""
	if in.KeyHash != "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return connectors.RelayAnswer{}, err
		}
		owner = hex.EncodeToString(nonce[:])
		lease := time.Minute
		if deadline, ok := ctx.Deadline(); ok {
			lease = time.Until(deadline) + time.Minute
		}
		for {
			answer, code, busy, err := s.claimPush(ctx, in, owner, lease)
			if err != nil {
				return connectors.RelayAnswer{}, err
			}
			if answer != nil {
				return *answer, pushError(code)
			}
			if !busy {
				break
			}
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return connectors.RelayAnswer{}, ctx.Err()
			case <-timer.C:
			}
		}
		defer func() {
			release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			// Completed answers clear owner_token; only an unfinished claim is released.
			_, _ = s.Pool.Exec(release, `DELETE FROM connector_push_answers WHERE organization=$1 AND connector_id=$2 AND key_hash=$3 AND owner_token=$4`, in.Organization, in.InstanceID, in.KeyHash, owner)
		}()
	}
	retry, err := s.takePushToken(ctx, in)
	if err != nil {
		return connectors.RelayAnswer{}, err
	}
	if retry > 0 {
		return connectors.RelayAnswer{Status: 429, ErrorCode: "rate_limited", RetryAfter: retry}, nil
	}
	answer, callErr := invoke()
	if owner == "" {
		return answer, callErr
	}
	code := ""
	if errors.Is(callErr, connectors.ErrPushItemRejected) {
		code = "item_rejected"
	} else if callErr != nil {
		return answer, callErr
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		return connectors.RelayAnswer{}, err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE connector_push_answers SET answer=$4,error_code=$5,expires_at=clock_timestamp()+$6::bigint*interval '1 microsecond',owner_token='' WHERE organization=$1 AND connector_id=$2 AND key_hash=$3 AND owner_token=$7`, in.Organization, in.InstanceID, in.KeyHash, raw, code, in.TTL.Microseconds(), owner)
	if err != nil {
		return connectors.RelayAnswer{}, err
	}
	if tag.RowsAffected() != 1 {
		return connectors.RelayAnswer{}, errors.New("push claim expired")
	}
	return answer, callErr
}

// claimPush either replays a completed answer, reserves a missing/expired key,
// or reports an active owner. The FK lock is released before plugin invocation.
func (s ConnectorStore) claimPush(ctx context.Context, in connectors.PushAttempt, owner string, lease time.Duration) (*connectors.RelayAnswer, string, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, "", false, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO connector_push_answers(organization,connector_id,key_hash,expires_at,owner_token) VALUES($1,$2,$3,clock_timestamp()+$4::bigint*interval '1 microsecond',$5) ON CONFLICT (organization,connector_id,key_hash) DO UPDATE SET key_hash=excluded.key_hash`, in.Organization, in.InstanceID, in.KeyHash, lease.Microseconds(), owner)
	if err != nil {
		return nil, "", false, err
	}
	var raw []byte
	var code, heldBy string
	var valid bool
	err = tx.QueryRow(ctx, `SELECT answer,error_code,owner_token,expires_at>clock_timestamp() FROM connector_push_answers WHERE organization=$1 AND connector_id=$2 AND key_hash=$3 FOR UPDATE`, in.Organization, in.InstanceID, in.KeyHash).Scan(&raw, &code, &heldBy, &valid)
	if err != nil {
		return nil, "", false, err
	}
	if raw != nil && valid {
		var answer connectors.RelayAnswer
		if err = json.Unmarshal(raw, &answer); err != nil {
			return nil, "", false, err
		}
		return &answer, code, false, tx.Commit(ctx)
	}
	if valid && heldBy != owner {
		return nil, "", true, tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `UPDATE connector_push_answers SET answer=NULL,error_code='',owner_token=$4,expires_at=clock_timestamp()+$5::bigint*interval '1 microsecond' WHERE organization=$1 AND connector_id=$2 AND key_hash=$3`, in.Organization, in.InstanceID, in.KeyHash, owner, lease.Microseconds())
	if err != nil {
		return nil, "", false, err
	}
	return nil, "", false, tx.Commit(ctx)
}

func pushError(code string) error {
	if code == "item_rejected" {
		return connectors.ErrPushItemRejected
	}
	if code != "" {
		return errors.New("invalid cached push answer")
	}
	return nil
}

func (s ConnectorStore) takePushToken(ctx context.Context, in connectors.PushAttempt) (time.Duration, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO connector_push_buckets(organization,connector_id,tokens,updated_at) VALUES($1,$2,$3,clock_timestamp()) ON CONFLICT DO NOTHING`, in.Organization, in.InstanceID, in.Burst)
	if err != nil {
		return 0, err
	}
	var tokens float64
	var updated, now time.Time
	err = tx.QueryRow(ctx, `SELECT tokens,updated_at FROM connector_push_buckets WHERE organization=$1 AND connector_id=$2 FOR UPDATE`, in.Organization, in.InstanceID).Scan(&tokens, &updated)
	if err != nil {
		return 0, err
	}
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return 0, err
	}
	tokens = math.Min(float64(in.Burst), tokens+math.Max(0, now.Sub(updated).Seconds())*in.RatePerSecond)
	var retry time.Duration
	if tokens < 1 {
		retry = time.Duration(math.Ceil((1-tokens)/in.RatePerSecond)) * time.Second
	} else {
		tokens--
	}
	_, err = tx.Exec(ctx, `UPDATE connector_push_buckets SET tokens=$3,updated_at=$4 WHERE organization=$1 AND connector_id=$2`, in.Organization, in.InstanceID, tokens, now)
	if err != nil {
		return 0, err
	}
	return retry, tx.Commit(ctx)
}

// RecordPush commits the journal event and all three rollup resolutions
// together. No payload, client IP, raw key or credential enters the journal.
func (s ConnectorStore) RecordPush(ctx context.Context, id string, received bool) error {
	var org, corpusID string
	err := s.Pool.QueryRow(ctx, `SELECT organization,corpus_id FROM connector_instances WHERE id=$1`, id).Scan(&org, &corpusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return corpus.ErrNotFound
	}
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	outcome := "refused"
	if received {
		outcome = "received"
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: "connector.push." + outcome, Resource: "connector", ResourceID: id, MutationID: hex.EncodeToString(nonce[:])}); err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	buckets := make([]int64, observability.Buckets)
	buckets[0] = 1
	for _, tier := range observability.Tiers {
		_, err = tx.Exec(ctx, `INSERT INTO observability_rollups AS r (organization,series,resolution_s,bucket_start,key,count,buckets) VALUES($1,$2,$3,$4,$5,1,$6) ON CONFLICT (organization,series,resolution_s,bucket_start,key) DO UPDATE SET count=r.count+1,buckets=ARRAY(SELECT coalesce(a,0)+coalesce(b,0) FROM unnest(r.buckets,excluded.buckets) WITH ORDINALITY AS t(a,b,i) ORDER BY i)`, org, observability.SeriesConnectorPush, int(tier.Resolution/time.Second), now.UTC().Truncate(tier.Resolution), observability.Key(id, outcome), buckets)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// PrunePushAnswers deletes completed keys past TTL, in bounded batches. Locked
// in-flight rows are skipped, so cleanup cannot interrupt a running delivery.
func (s ConnectorStore) PrunePushAnswers(ctx context.Context) error {
	for {
		tag, err := s.Pool.Exec(ctx, `DELETE FROM connector_push_answers WHERE (organization,connector_id,key_hash) IN (SELECT organization,connector_id,key_hash FROM connector_push_answers WHERE expires_at<now() ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED)`)
		if err != nil || tag.RowsAffected() < 1000 {
			return err
		}
	}
}
