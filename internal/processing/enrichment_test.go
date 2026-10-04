package processing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/processing"
)

// oneVersion is a Version already searchable by keyword, waiting for its
// vectors: the content store as enrichment reads and updates it.
type oneVersion struct {
	content.ReceiptReader
	content.RecordReader
	content.VersionReader
	content.EmbeddingRepository
	progress []string
	timeouts int
	// enriched is when the Version's enrichment succeeded; nil until then.
	enriched *time.Time
}

var manifest = content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "a long article"}}}}

func (s *oneVersion) Receipt(context.Context, string, string) (content.Receipt, error) {
	return content.Receipt{RecordID: "record", VersionID: "version", Source: content.Source{CorpusID: "corpus"}}, nil
}
func (s *oneVersion) Version(context.Context, string, string, string) (content.StoredVersion, error) {
	return content.StoredVersion{RecordID: "record", ID: "version", CorpusID: "corpus", ManifestBlob: content.Blob{Key: "manifest"}, TextBlob: content.Blob{Key: "text"},
		Availability: content.Availability{State: "retrieval_ready", Searchable: true}, Steps: content.Steps{Enriched: s.enriched}}, nil
}
func (s *oneVersion) Record(context.Context, string, string) (content.Record, error) {
	return content.Record{ID: "record", Source: content.Source{CorpusID: "corpus"}}, nil
}
func (s *oneVersion) Put(context.Context, string, []byte) (content.Blob, error) {
	return content.Blob{}, errors.New("read only")
}
func (s *oneVersion) Read(_ context.Context, b content.Blob) ([]byte, error) {
	if b.Key == "manifest" {
		return json.Marshal(manifest)
	}
	return []byte(content.NormalizedText(manifest)), nil
}
func (s *oneVersion) EnrichmentProgress(_ context.Context, _, _, state, code string) error {
	s.progress = append(s.progress, state+" "+code)
	return nil
}
func (s *oneVersion) CountEnrichmentTimeout(context.Context, string, string) (int, error) {
	s.timeouts++
	return s.timeouts, nil
}

// plugin is an ingestion plugin whose next calls fail with the given errors.
type plugin struct {
	processing.IngestionPlugin
	errs []error
}

func (p *plugin) Descriptor() processing.IngestionDescriptor {
	return processing.IngestionDescriptor{Recipe: "plugin:acme@1", VectorSpaces: map[string]content.VectorSpace{"acme.space@1": {ID: "acme.space@1"}}}
}
func (p *plugin) SegmentAndEmbed(context.Context, string, string, content.Version, []string) ([]processing.PluginSegment, error) {
	err := p.errs[0]
	p.errs = p.errs[1:]
	return nil, err
}

type served struct{}

func (served) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation{ID: "generation", SpaceID: "acme.space@1"}, nil
}

// A plugin that is reachable but never finishes a Version stops being retried
// after EnrichmentTimeoutBudget deadlines: enrichment is blocked with
// enrichment_timeout, and nothing else of the Version changes, so it stays
// searchable by keyword. An unavailable plugin is an outage and never counts.
func TestEnrichmentStopsAfterRepeatedPluginDeadlines(t *testing.T) {
	store := &oneVersion{}
	deadline := fmt.Errorf("plugin_unavailable: %w: POST segment_and_embed: context deadline exceeded", processing.ErrPluginDeadline)
	errs := []error{deadline, errors.New("plugin_unavailable: connection refused")}
	for range processing.EnrichmentTimeoutBudget - 1 {
		errs = append(errs, deadline)
	}
	p := &plugin{errs: errs}
	service := processing.Service{Content: content.Service{Receipts: store, RecordStore: store, Versions: store, Blobs: store, Embeddings: store},
		Plugin: &processing.PluginDeriver{Plugin: p}, Routing: served{}}
	var results []error
	for len(p.errs) > 0 {
		results = append(results, service.Enrich(context.Background(), "org", "receipt"))
	}
	last := len(results) - 1
	for n, err := range results[:last] {
		if err == nil {
			t.Fatalf("attempt %d ended enrichment; want a retry", n+1)
		}
	}
	if results[last] != nil {
		t.Fatalf("attempt %d: %v; want enrichment to stop", last+1, results[last])
	}
	if got := store.progress[len(store.progress)-1]; got != "blocked "+content.CodeEnrichmentTimeout {
		t.Fatalf("enrichment ended %q; progress %q", got, store.progress)
	}
	if store.timeouts != processing.EnrichmentTimeoutBudget {
		t.Fatalf("%d deadlines counted, want %d: the outage must not count", store.timeouts, processing.EnrichmentTimeoutBudget)
	}
}

// A retried enrichment of a Version whose enrichment already succeeded ends at
// once, whatever plan the worker follows now: a worker killed between the
// success and its report to Temporal must not leave the retry deriving again
// and failing forever, so the activity releases the pin and the plugin
// version can leave draining (THE-861).
func TestEnrichmentAlreadySucceededEndsTheRetry(t *testing.T) {
	enriched := time.Now()
	store := &oneVersion{enriched: &enriched}
	p := &plugin{errs: []error{errors.New("derived on another plan: projection missing")}}
	service := processing.Service{Content: content.Service{Receipts: store, RecordStore: store, Versions: store, Blobs: store, Embeddings: store},
		Plugin: &processing.PluginDeriver{Plugin: p}, Routing: served{}}
	if err := service.Enrich(context.Background(), "org", "receipt"); err != nil {
		t.Fatalf("retried enrichment of an enriched Version: %v; want it to end", err)
	}
	if len(p.errs) == 0 || len(store.progress) > 0 {
		t.Fatalf("an enriched Version was enriched again: plugin called %t, progress %q", len(p.errs) == 0, store.progress)
	}
}
