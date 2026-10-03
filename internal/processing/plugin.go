package processing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// IngestionPlugin is the pinned ingestion plugin as the engine calls it. Its
// spaces are named by key ("<space id>@<space version>").
type IngestionPlugin interface {
	// Recipe names the plugin's segmentation: its id and version.
	Recipe() string
	// Producer names the producer of its Embedding Artifacts.
	Producer() string
	// Provenance is recorded with each Segmentation it makes.
	Provenance() json.RawMessage
	// Owns reports whether a space key is one of the plugin's spaces.
	Owns(space string) bool
	// VectorSpace describes one of its spaces, for its Embedding Artifacts.
	VectorSpace(space string) (content.VectorSpace, bool)
	// Spaces are the space keys the deployment enables, the served one first.
	Spaces() []string
	// SegmentsOnly reports whether the plugin answers a request with no
	// space (Plugin API 0.7): its segments alone, without vectors.
	SegmentsOnly() bool
	// SegmentAndEmbed cuts a Version's text Parts and embeds each segment in
	// the given spaces (none: the segments alone, when SegmentsOnly). A
	// terminal refusal or an answer the engine refuses is
	// content.ErrIngestionRefused; any other error is retried.
	SegmentAndEmbed(ctx context.Context, org, corpusID string, v content.Version, spaces []string) ([]PluginSegment, error)
}

// PluginSegment is one segment a plugin returned, with a vector per space key.
type workBinder interface{ Bind(context.Context) error }

func (d PluginDeriver) bind(ctx context.Context) error {
	if p, ok := d.Plugin.(workBinder); ok {
		return p.Bind(ctx)
	}
	return nil
}

type PluginSegment struct {
	content.SegmentInput
	Vectors map[string][]float32
}

// PluginDeriver derives Versions through the pinned ingestion plugin for
// generations whose served space it owns. It stores the Segmentation and an
// Embedding Artifact per segment and space, so enrichment and rebuilds reuse
// them and call the plugin again only for what is missing.
type PluginDeriver struct {
	Content content.Service
	Plugin  IngestionPlugin
}

// Routed resolves ingestion by the Version's accepted source media type,
// and resolves query/backfill work by the vector space's owner.
type Routed interface {
	Ingestor(context.Context, string) IngestionPlugin
	ForSpace(context.Context, string) IngestionPlugin
}

func (d PluginDeriver) forVersion(ctx context.Context, v content.Version) PluginDeriver {
	if r, ok := d.Plugin.(Routed); ok {
		d.Plugin = r.Ingestor(ctx, v.SourceMediaType)
		return d
	}
	return d.resolved(ctx)
}
func (d PluginDeriver) forSpace(ctx context.Context, space string) PluginDeriver {
	if r, ok := d.Plugin.(Routed); ok {
		d.Plugin = r.ForSpace(ctx, space)
		return d
	}
	return d.resolved(ctx)
}

type ingestionFailure struct {
	plugin IngestionPlugin
	cause  error
}

func (e *ingestionFailure) Error() string { return e.cause.Error() }
func (e *ingestionFailure) Unwrap() error { return e.cause }
func (d PluginDeriver) failure(err error) error {
	if err == nil {
		return nil
	}
	return &ingestionFailure{plugin: d.Plugin, cause: err}
}

// Bound resolves one source route (or a backfill target owner) for an entire step.
func (d PluginDeriver) Bound(ctx context.Context, mediaType string, spaces []string) DerivationDriver {
	if len(spaces) > 0 {
		return d.forSpace(ctx, spaces[0])
	}
	return d.forVersion(ctx, content.Version{SourceMediaType: mediaType})
}
func (d PluginDeriver) ServedSpace(g content.Generation) string {
	if d.Plugin == nil {
		return ""
	}
	return g.ServedFor(content.PluginOfRecipe(d.Plugin.Recipe()))
}

// Serves checks the routed owner's served space of a generation.
func (d PluginDeriver) Serves(ctx context.Context, v content.Version, g content.Generation) error {
	d = d.forVersion(ctx, v)
	if d.Plugin == nil || !d.owns(g.ServedFor(content.PluginOfRecipe(d.Plugin.Recipe()))) {
		return d.failure(ErrSpaceUnowned)
	}
	return nil
}

// Pinned is an ingestion plugin that decides whether work pinned to its plan
// stops after a call failed with cause: a diagnostic when the plugin stayed
// unreachable, or can no longer serve the work, after it left the active
// plan (plugins.Unreachable); nil to keep retrying.
type Pinned interface {
	Gone(ctx context.Context, cause error) (*content.Diagnostic, error)
}

func (d PluginDeriver) resolved(ctx context.Context) PluginDeriver {
	if r, ok := d.Plugin.(Routed); ok {
		d.Plugin = r.Ingestor(ctx, "")
	}
	return d
}

// Gone is the diagnostic that stops work pinned to a plan after cause
// (Pinned), or nil to keep retrying.
func (d PluginDeriver) Gone(ctx context.Context, cause error) (*content.Diagnostic, error) {
	var failure *ingestionFailure
	if errors.As(cause, &failure) {
		d.Plugin = failure.plugin
	} else {
		d = d.resolved(ctx)
	}
	if p, ok := d.Plugin.(Pinned); ok {
		return p.Gone(ctx, cause)
	}
	return nil, nil
}

// Owns reports whether the plugin the plan names for ctx owns a space.
func (d PluginDeriver) Owns(ctx context.Context, space string) bool {
	return d.Plugin != nil && d.forSpace(ctx, space).owns(space)
}

func (d PluginDeriver) owns(space string) bool { return d.Plugin != nil && d.Plugin.Owns(space) }

// Segment returns a Version's plugin segmentation for its baseline: the
// stored one, or else the plugin's segments alone, stored, so the Version is
// searchable by keyword without waiting for any embedding backend. A plugin
// limited to Plugin API 0.6 answers segments and vectors in one call; both
// are stored, the vectors in the generation's spaces it owns or, for a
// generation it does not serve, in the spaces the deployment enables.
func (d PluginDeriver) Segment(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (seg content.Segmentation, err error) {
	d = d.forVersion(ctx, v)
	defer func() { err = d.failure(err) }()
	if err = d.bind(ctx); err != nil {
		return
	}
	if d.Plugin == nil {
		return seg, ErrSpaceUnowned
	}
	seg, err = d.Content.PluginSegmentationOf(ctx, org, v, d.Plugin.Recipe())
	if !errors.Is(err, corpus.ErrNotFound) {
		return seg, err
	}
	if !d.Plugin.SegmentsOnly() {
		spaces := d.owned(g)
		if len(spaces) == 0 {
			spaces = d.Plugin.Spaces()
		}
		seg, _, err = d.derive(ctx, org, corpusID, v, spaces)
		return seg, err
	}
	segments, err := d.Plugin.SegmentAndEmbed(ctx, org, corpusID, v, nil)
	if err != nil {
		return seg, err
	}
	return d.save(ctx, org, v, segments)
}

// owned lists the generation's spaces the pinned plugin owns.
func (d PluginDeriver) owned(g content.Generation) []string {
	var spaces []string
	for _, key := range g.VectorSpaces() {
		if d.Plugin.Owns(key) {
			spaces = append(spaces, key)
		}
	}
	return spaces
}

// Derive returns a Version's plugin segmentation and its vectors in every
// space of the generation the plugin owns, the served one included.
func (d PluginDeriver) Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (seg content.Segmentation, data []content.EmbeddingData, err error) {
	d = d.forVersion(ctx, v)
	defer func() { err = d.failure(err) }()
	if err = d.bind(ctx); err != nil {
		return
	}
	if d.Plugin == nil {
		return seg, nil, ErrSpaceUnowned
	}
	spaces := d.owned(g)
	if !d.owns(g.ServedFor(content.PluginOfRecipe(d.Plugin.Recipe()))) || len(spaces) == 0 {
		return content.Segmentation{}, nil, fmt.Errorf("%w: the pinned ingestion plugin does not own space %s", ErrSpaceUnowned, g.SpaceID)
	}
	return d.derive(ctx, org, corpusID, v, spaces)
}

// derive reuses the stored segmentation and artifacts and calls the plugin
// only when a vector is missing. The plugin's segments must then be the
// stored ones: a plugin that answers other segments is refused.
func (d PluginDeriver) derive(ctx context.Context, org, corpusID string, v content.Version, spaces []string) (content.Segmentation, []content.EmbeddingData, error) {
	seg, err := d.Content.PluginSegmentationOf(ctx, org, v, d.Plugin.Recipe())
	switch {
	case err == nil:
		data, complete, err := d.stored(ctx, org, corpusID, v, seg, spaces)
		if err != nil || complete {
			return seg, data, err
		}
	case !errors.Is(err, corpus.ErrNotFound):
		return seg, nil, err
	}
	segments, err := d.Plugin.SegmentAndEmbed(ctx, org, corpusID, v, spaces)
	if err != nil {
		return seg, nil, err
	}
	seg, err = d.save(ctx, org, v, segments)
	if err != nil {
		return seg, nil, err
	}
	data := make([]content.EmbeddingData, 0, len(seg.Segments)*len(spaces))
	for i, p := range seg.Segments {
		for _, key := range spaces {
			space, _ := d.Plugin.VectorSpace(key)
			vector := segments[i].Vectors[key]
			input := content.EmbeddingInput(org, corpusID, v, seg, p, space, d.Plugin.Producer())
			artifact, err := d.Content.SaveEmbedding(ctx, input, space, vector)
			if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrInvalid) {
				return seg, nil, content.Refused("the ingestion plugin answered a vector that differs from the stored artifact")
			}
			if err != nil {
				return seg, nil, err
			}
			data = append(data, content.EmbeddingData{Artifact: artifact, Vector: vector})
		}
	}
	return seg, data, nil
}

// ErrSegmentsDiffer reports that the plugin cuts a Version into other
// segments than the segmentation being filled: only a rebuild can move the
// Version to them.
var ErrSegmentsDiffer = errors.New("segments differ")

// Fill returns the vectors of seg's segments in the given spaces, for a
// backfill. seg is the segmentation a generation projects, which an earlier
// version of the plugin may have made. Stored artifacts are reused;
// otherwise the plugin embeds the Version in those spaces only, and its
// segments must have seg's Parts and offsets (ErrSegmentsDiffer). Each
// vector is stored as an Embedding Artifact of seg's segment.
func (d PluginDeriver) Fill(ctx context.Context, org, corpusID string, v content.Version, seg content.Segmentation, spaces []string) (_ []content.EmbeddingData, err error) {
	if len(spaces) > 0 {
		d = d.forSpace(ctx, spaces[0])
	} else {
		d = d.resolved(ctx)
	}
	defer func() { err = d.failure(err) }()
	for _, key := range spaces {
		if !d.owns(key) {
			return nil, fmt.Errorf("%w: the pinned ingestion plugin does not own space %s", ErrSpaceUnowned, key)
		}
	}
	data, complete, err := d.stored(ctx, org, corpusID, v, seg, spaces)
	if err != nil || complete {
		return data, err
	}
	segments, err := d.Plugin.SegmentAndEmbed(ctx, org, corpusID, v, spaces)
	if err != nil {
		return nil, err
	}
	if len(segments) != len(seg.Segments) {
		return nil, ErrSegmentsDiffer
	}
	for i, p := range seg.Segments {
		if s := segments[i]; s.PartKey != p.PartKey || s.Start != p.Start || s.End != p.End {
			return nil, ErrSegmentsDiffer
		}
	}
	data = make([]content.EmbeddingData, 0, len(seg.Segments)*len(spaces))
	for i, p := range seg.Segments {
		for _, key := range spaces {
			space, _ := d.Plugin.VectorSpace(key)
			vector := segments[i].Vectors[key]
			artifact, err := d.Content.SaveEmbedding(ctx, content.EmbeddingInput(org, corpusID, v, seg, p, space, d.Plugin.Producer()), space, vector)
			if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrInvalid) {
				return nil, fmt.Errorf("%w: a vector differs from the stored artifact", content.ErrIngestionRefused)
			}
			if err != nil {
				return nil, err
			}
			data = append(data, content.EmbeddingData{Artifact: artifact, Vector: vector})
		}
	}
	return data, nil
}

// FillIndependent derives an owner's own segmentation and vectors for a
// backfill. Unlike Fill, it does not require the owner to serve the
// generation: an evaluation owner may have no projection yet, so derive
// creates its segmentation and artifacts before the caller publishes them.
// When the owner's segmentation and artifacts already exist, derive reuses
// them and preserves its stored-cut conflict policy.
func (d PluginDeriver) FillIndependent(ctx context.Context, org, corpusID string, v content.Version, spaces []string) (_ content.Segmentation, _ []content.EmbeddingData, err error) {
	if len(spaces) == 0 {
		return content.Segmentation{}, nil, ErrSpaceUnowned
	}
	d = d.forSpace(ctx, spaces[0])
	defer func() {
		err = d.failure(err)
	}()
	if err = d.bind(ctx); err != nil {
		return content.Segmentation{}, nil, err
	}
	if d.Plugin == nil {
		return content.Segmentation{}, nil, ErrSpaceUnowned
	}
	for _, key := range spaces {
		if !d.owns(key) {
			return content.Segmentation{}, nil, fmt.Errorf("%w: the pinned ingestion plugin does not own space %s", ErrSpaceUnowned, key)
		}
	}
	return d.derive(ctx, org, corpusID, v, spaces)
}

// save stores the plugin's segments as the Version's segmentation. Another
// answer already stored for the Version is a refusal: the plugin is not
// deterministic, and search would change under a rebuild.
func (d PluginDeriver) save(ctx context.Context, org string, v content.Version, segments []PluginSegment) (content.Segmentation, error) {
	inputs := make([]content.SegmentInput, len(segments))
	for i, s := range segments {
		inputs[i] = s.SegmentInput
	}
	seg, err := content.PluginSegmentation(org, v, d.Plugin.Recipe(), d.Plugin.Provenance(), inputs)
	if err != nil {
		return seg, content.Refused("the ingestion plugin answered segments outside the Version's text Parts")
	}
	if err = d.Content.SaveSegmentation(ctx, org, v, seg); err != nil {
		if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrInvalid) {
			return seg, content.Refused("the ingestion plugin answered segments that differ from the segmentation stored for this Version")
		}
		return seg, err
	}
	return seg, nil
}

// stored loads the verified artifact of every segment in every space; it is
// incomplete when one is missing.
func (d PluginDeriver) stored(ctx context.Context, org, corpusID string, v content.Version, seg content.Segmentation, spaces []string) ([]content.EmbeddingData, bool, error) {
	data := make([]content.EmbeddingData, 0, len(seg.Segments)*len(spaces))
	for _, p := range seg.Segments {
		for _, key := range spaces {
			space, _ := d.Plugin.VectorSpace(key)
			input := content.EmbeddingInput(org, corpusID, v, seg, p, space, d.Plugin.Producer())
			artifact, vector, err := d.Content.LoadEmbedding(ctx, org, input.DerivationID)
			switch {
			case errors.Is(err, corpus.ErrNotFound), errors.Is(err, content.ErrArtifactMissing):
				return nil, false, nil
			case err != nil:
				return nil, false, err
			}
			data = append(data, content.EmbeddingData{Artifact: artifact, Vector: vector})
		}
	}
	return data, true, nil
}
