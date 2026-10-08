package postgres

import (
	"context"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"slices"
)

type journalFinish func() error
type journalPreparation func(context.Context, pgx.Tx) (journalFinish, error)
type journalGroupKey struct{}
type journalEvent struct {
	eventInput
	trace string
}
type journalGroup struct {
	organization   string
	locked         bool
	events         []journalEvent
	records        map[string]bool
	beforeFinishes []journalFinish
}

var errJournalReplay = errors.New("journal receipt already resolved")

func journalGroupOf(ctx context.Context) *journalGroup {
	group, _ := ctx.Value(journalGroupKey{}).(*journalGroup)
	return group
}

// CommitIngestion reports success per member only after a durable group commit.
// A failed group has rolled back in full; single-member retries isolate poison
// without retaining another receipt's prepared objects or journal positions.
func (s MaterializationStore) CommitIngestion(ctx context.Context, entries []content.IngestionCommit) []error {
	outcomes := make([]error, len(entries))
	err := s.commitIngestionGroup(ctx, entries)
	if err == nil {
		return outcomes
	}
	for i, e := range entries {
		if e.Context == nil {
			outcomes[i] = content.ErrInvalid
			continue
		}
		if e.Context.Err() != nil {
			outcomes[i] = e.Context.Err()
			continue
		}
		// A cancelled sibling may abort the group, but healthy members retain
		// their own activity/stage context and can commit independently.
		if e.Kind == content.CommitPublication {
			outcomes[i] = s.commitIngestionGroup(e.Context, []content.IngestionCommit{e})
			continue
		}
		outcomes[i] = retryJournalWrite(e.Context, "ingestion fallback", func(ctx context.Context) error {
			switch e.Kind {
			case content.CommitMaterialization:
				return s.publishAttempt(ctx, e.Publication.Work, e.Publication.Publication)
			case content.CommitBaseline:
				return (ProjectionStore{Pool: s.Pool}).promoteAttempt(ctx, e.Organization, e.Segmentation, e.Generation)
			case content.CommitVectors:
				return (EmbeddingStore{Pool: s.Pool}).commitEnrichmentAttempt(ctx, e.Organization, e.Segmentation, e.Generation, e.Artifacts)
			default:
				return content.ErrInvalid
			}
		})
	}
	return outcomes
}

type ingestionMemberContext struct {
	context.Context
	values context.Context
}

func (c ingestionMemberContext) Value(key any) any { return c.values.Value(key) }

func (s MaterializationStore) commitIngestionGroup(ctx context.Context, entries []content.IngestionCommit) error {
	if len(entries) == 0 {
		return nil
	}
	if len(entries) > 16 {
		return content.ErrInvalid
	}
	org := entries[0].Organization
	seen := map[string]bool{}
	preparations := make([]journalPreparation, len(entries))
	for i, e := range entries {
		if e.Organization != org || e.RecordID == "" || seen[e.RecordID] || e.Context == nil {
			return content.ErrInvalid
		}
		seen[e.RecordID] = true
		preparations[i] = func(groupCtx context.Context, tx pgx.Tx) (journalFinish, error) {
			member := context.WithValue(ingestionMemberContext{groupCtx, e.Context}, journalGroupKey{}, journalGroupOf(groupCtx))
			switch e.Kind {
			case content.CommitPublication, content.CommitMaterialization:
				w, p := e.Publication.Work, e.Publication.Publication
				if w.Organization != org || w.RecordID != e.RecordID || p.Quarantine != nil {
					return nil, content.ErrInvalid
				}
				finish, err := preparePublication(member, tx, w, p)
				if finish == nil && err == nil && e.Kind == content.CommitPublication {
					// Another worker may have materialized the receipt while its
					// provider ran. Resolution alone does not prove keyword readiness.
					return nil, ErrGenerationChanged
				}
				if err != nil || finish == nil || e.Kind == content.CommitMaterialization {
					return finish, err
				}
				seg, g := e.Publication.Segmentation, e.Publication.Generation
				if seg.VersionID != w.VersionID {
					return nil, content.ErrInvalid
				}
				segmented, err := prepareSegmentation(member, tx, org, seg)
				if err != nil {
					return nil, err
				}
				return func() error {
					if err := finish(); err != nil {
						return err
					}
					if segmented != nil {
						if err := segmented(); err != nil {
							return err
						}
					}
					return promoteCanonical(member, tx, org, seg, g)
				}, nil
			case content.CommitBaseline:
				return func() error { return promoteCanonical(member, tx, org, e.Segmentation, e.Generation) }, nil
			case content.CommitVectors:
				return prepareEnrichment(member, tx, org, e.Segmentation, e.Generation, e.Artifacts)
			default:
				return nil, content.ErrInvalid
			}
		}
	}
	return commitJournalGroup(ctx, s.Pool, org, preparations)
}

func commitJournalGroup(ctx context.Context, pool *pgxpool.Pool, org string, preparations []journalPreparation) error {
	return retryJournalWrite(ctx, "ingestion group", func(ctx context.Context) error {
		tx, err := database(ctx, pool).Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.WithoutCancel(ctx))
		if err = lockProjectionRouting(ctx, tx); err != nil {
			return err
		}
		group := &journalGroup{organization: org, records: map[string]bool{}}
		ctx = context.WithValue(ctx, journalGroupKey{}, group)
		finishes := make([]journalFinish, 0, len(preparations))
		for _, prepare := range preparations {
			finish, err := prepare(ctx, tx)
			if err != nil {
				return err
			}
			if finish != nil {
				finishes = append(finishes, finish)
			}
		}
		if len(finishes) == 0 {
			return nil
		}
		if err = lockJournal(ctx, tx, org); err != nil {
			return err
		}
		group.locked = true
		for _, finish := range group.beforeFinishes {
			if err = finish(); err != nil {
				return err
			}
		}
		for _, finish := range finishes {
			if err = finish(); err != nil {
				return err
			}
		}
		if err = group.append(ctx, tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

// Allocate a contiguous range and observe final canonical state once per
// Record. Every event retains its member's trace.
func (group *journalGroup) append(ctx context.Context, tx pgx.Tx) error {
	writes := &pgx.Batch{}
	if len(group.events) > 0 {
		ids, corpora, kinds, resources, records, versions, traces := []string{}, []string{}, []string{}, []string{}, []string{}, []string{}, []string{}
		for _, e := range group.events {
			ids = append(ids, eventID(e.eventInput))
			corpora = append(corpora, e.CorpusID)
			kinds = append(kinds, e.Kind)
			resources = append(resources, e.Resource)
			records = append(records, e.ResourceID)
			versions = append(versions, e.VersionID)
			traces = append(traces, e.trace)
		}
		writes.Queue(`WITH positions AS (
 UPDATE organization_journals SET last_sequence=last_sequence+$2 WHERE organization=$1 RETURNING last_sequence-$2 AS start
) INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id,trace_context)
 SELECT $1,positions.start+e.n,e.id,e.corpus,e.kind,e.resource,e.record,NULLIF(e.version,''),e.trace
 FROM positions CROSS JOIN unnest($3::text[],$4::text[],$5::text[],$6::text[],$7::text[],$8::text[],$9::text[])
 WITH ORDINALITY e(id,corpus,kind,resource,record,version,trace,n) ORDER BY e.n`, group.organization, len(group.events), ids, corpora, kinds, resources, records, versions, traces)
	}
	orgs, records := []string{}, []string{}
	for record := range group.records {
		records = append(records, record)
	}
	slices.Sort(records)
	for range records {
		orgs = append(orgs, group.organization)
	}
	if len(records) > 0 {
		queueRecordObservations(writes, orgs, records)
	}
	if len(group.events) > 0 {
		writes.Queue(acknowledgeQueueJournalSQL, group.organization, len(group.events))
	}
	return tx.SendBatch(ctx, writes).Close()
}
