package content

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

var ErrCountTooBroad = publicerr.RecordCountTooBroad

type SourceStatsKey struct{ Namespace, ConnectorID string }
type CorpusStatsQuery struct {
	Histogram, Sources bool
	Hourly             bool
	From, To           *time.Time
	HistoryAfter       *time.Time
	SourceAfter        *SourceStatsKey
	SourceLimit        int
}
type RecordCountResult struct {
	Count       int64     `json:"count"`
	ObservedAt  time.Time `json:"observed_at"`
	Approximate bool      `json:"approximate"`
	Complete    bool      `json:"complete"`
}
type CorpusStats struct {
	CorpusID            string          `json:"corpus_id"`
	Total               int64           `json:"total"`
	CatalogTotal        int64           `json:"catalog_total"`
	UndatedTotal        int64           `json:"undated_total"`
	CatalogUndatedTotal int64           `json:"catalog_undated_total"`
	FirstCatalogHour    *time.Time      `json:"first_catalog_hour,omitempty"`
	LastCatalogHour     *time.Time      `json:"last_catalog_hour,omitempty"`
	ObservedAt          time.Time       `json:"observed_at"`
	Approximate         bool            `json:"approximate"`
	Complete            bool            `json:"complete"`
	Histogram           *StatsHistogram `json:"histogram,omitempty"`
	Sources             *StatsSources   `json:"sources,omitempty"`
}
type StatsBucket struct {
	Start        time.Time `json:"start"`
	Count        int64     `json:"count"`
	CatalogCount int64     `json:"catalog_count"`
}
type StatsHistogram struct {
	From              time.Time     `json:"from"`
	To                time.Time     `json:"to"`
	ResolutionSeconds int           `json:"resolution_seconds"`
	Items             []StatsBucket `json:"items"`
	NextPageCursor    string        `json:"next_page_cursor,omitempty"`
	Next              *time.Time    `json:"-"`
}
type StatsSource struct {
	Namespace    string `json:"namespace"`
	ConnectorID  string `json:"connector_id"`
	Count        int64  `json:"count"`
	CatalogCount int64  `json:"catalog_count"`
}
type StatsSources struct {
	Items          []StatsSource   `json:"items"`
	NextPageCursor string          `json:"next_page_cursor,omitempty"`
	Next           *SourceStatsKey `json:"-"`
}
type CorpusStatsReader interface {
	CorpusStats(context.Context, string, string, CorpusStatsQuery) (CorpusStats, error)
	RecordCount(context.Context, string, string, RecordQuery) (RecordCountResult, error)
}

func (s Service) CorpusStats(ctx context.Context, scope corpus.Scope, id string, q CorpusStatsQuery) (CorpusStats, error) {
	if err := s.authorizeCatalog(ctx, scope, id); err != nil {
		return CorpusStats{}, err
	}
	if s.Stats == nil {
		return CorpusStats{}, ErrUnsupported
	}
	return s.Stats.CorpusStats(ctx, scope.Organization, id, q)
}
func (s Service) RecordCount(ctx context.Context, scope corpus.Scope, id string, q RecordQuery) (RecordCountResult, error) {
	if err := s.authorizeCatalog(ctx, scope, id); err != nil {
		return RecordCountResult{}, err
	}
	if s.Stats == nil {
		return RecordCountResult{}, ErrUnsupported
	}
	return s.Stats.RecordCount(ctx, scope.Organization, id, q)
}
