package observability

import (
	"context"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// ScopedReader keeps admin rollup reads bound to an authorized Organization.
// Read authorizes the complete read once, before the caller loads its query.
type ScopedReader struct {
	reader Reader
	org    string
}

func (r Reader) Read(scope corpus.Scope, query func(ScopedReader) error) error {
	if err := scope.Require(corpus.ActionStatsRead); err != nil {
		return err
	}
	if r.Store == nil {
		return corpus.ErrNotFound
	}
	return query(ScopedReader{reader: r, org: scope.Organization})
}
func (r ScopedReader) Report(ctx context.Context, series string, w Window, keys ...string) (Report, error) {
	return r.reader.Report(ctx, r.org, series, w, keys...)
}
func (r ScopedReader) Counts(ctx context.Context, series string, w Window, limit int) (Counts, error) {
	return r.reader.Counts(ctx, r.org, series, w, limit)
}
func (r ScopedReader) TopQueries(ctx context.Context, w Window, limit int) (Counts, error) {
	return r.reader.TopQueries(ctx, r.org, w, limit)
}
