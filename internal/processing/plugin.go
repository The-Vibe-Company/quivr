package processing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// IngestionPlugin is the pinned ingestion plugin as the engine calls it. Its
// spaces are named by key ("<space id>@<space version>").
type IngestionPlugin interface {
	Descriptor() IngestionDescriptor
	// SegmentAndEmbed cuts a Version's text Parts and embeds each segment in
	// the given spaces (none: the segments alone, when SegmentsOnly). A
	// terminal refusal or an answer the engine refuses is
	// content.ErrIngestionRefused; any other error is retried.
	SegmentAndEmbed(ctx context.Context, org, corpusID string, v content.Version, spaces []string) ([]PluginSegment, error)
}

// IngestionDescriptor is the metadata of one resolved ingestion owner. Spaces
// lists enabled keys in served-first order; VectorSpaces includes every declared
// space. Treat its maps and slices as read-only and keep it with the plugin
// for the entire logical work step.
type IngestionDescriptor struct {
	PluginID       string
	PluginVersion  string
	RegistrationID string
	Configuration  json.RawMessage
	// InputPrices names every declared space; nil means its price is unknown.
	InputPrices  map[string]*float64
	Recipe       string
	Producer     string
	Provenance   json.RawMessage
	Spaces       []string
	VectorSpaces map[string]content.VectorSpace
	SegmentsOnly bool
}

func (d IngestionDescriptor) Owns(space string) bool {
	_, ok := d.VectorSpaces[space]
	return ok
}

func (d IngestionDescriptor) VectorSpace(space string) (content.VectorSpace, bool) {
	value, ok := d.VectorSpaces[space]
	return value, ok
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
	Content    content.Service
	Plugin     IngestionPlugin
	descriptor *IngestionDescriptor
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
		d.descriptor = nil
		return d.described()
	}
	return d.resolved(ctx)
}
func (d PluginDeriver) forSpace(ctx context.Context, space string) PluginDeriver {
	if r, ok := d.Plugin.(Routed); ok {
		d.Plugin = r.ForSpace(ctx, space)
		d.descriptor = nil
		return d.described()
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
	return g.ServedFor(content.PluginOfRecipe(d.described().descriptor.Recipe))
}

// Serves checks the routed owner's served space of a generation.
func (d PluginDeriver) Serves(ctx context.Context, v content.Version, g content.Generation) error {
	d = d.forVersion(ctx, v)
	if d.Plugin == nil || !d.owns(g.ServedFor(content.PluginOfRecipe(d.descriptor.Recipe))) {
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
		d.descriptor = nil
	}
	return d.described()
}

func (d PluginDeriver) described() PluginDeriver {
	if d.Plugin != nil && d.descriptor == nil {
		descriptor := d.Plugin.Descriptor()
		d.descriptor = &descriptor
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

func (d PluginDeriver) owns(space string) bool { return d.Plugin != nil && d.descriptor.Owns(space) }

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
	seg, err = d.Content.PluginSegmentationOf(ctx, org, v, d.descriptor.Recipe)
	if !errors.Is(err, corpus.ErrNotFound) {
		return seg, err
	}
	if !d.descriptor.SegmentsOnly {
		spaces := d.owned(g)
		if len(spaces) == 0 {
			spaces = d.descriptor.Spaces
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
		if d.descriptor.Owns(key) {
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
	if !d.owns(g.ServedFor(content.PluginOfRecipe(d.descriptor.Recipe))) || len(spaces) == 0 {
		return content.Segmentation{}, nil, fmt.Errorf("%w: the pinned ingestion plugin does not own space %s", ErrSpaceUnowned, g.SpaceID)
	}
	return d.derive(ctx, org, corpusID, v, spaces)
}

// derive reuses the stored segmentation and artifacts and calls the plugin
// only when a vector is missing. The plugin's segments must then be the
// stored ones: a plugin that answers other segments is refused.
func (d PluginDeriver) derive(ctx context.Context, org, corpusID string, v content.Version, spaces []string) (content.Segmentation, []content.EmbeddingData, error) {
	seg, err := d.Content.PluginSegmentationOf(ctx, org, v, d.descriptor.Recipe)
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
			space, _ := d.descriptor.VectorSpace(key)
			vector := segments[i].Vectors[key]
			input := content.EmbeddingInput(org, corpusID, v, seg, p, space, d.descriptor.Producer)
			artifact, vector, err := d.saveOrAdopt(ctx, org, input, space, vector)
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

// saveOrAdopt stores a segment's vector, or adopts the artifact another
// derivation of the same input stored first, for example an ingestion running
// beside a rebuild. Embedding providers are not bitwise deterministic, so the
// first stored artifact is canonical. Saving the stored vector again yields
// that same artifact and still checks the registered space, so a space whose
// manifest changed, or an unreadable or differently sized stored vector,
// keeps the conflict.
func (d PluginDeriver) saveOrAdopt(ctx context.Context, org string, input content.Embedding, space content.VectorSpace, vector []float32) (content.Embedding, []float32, error) {
	artifact, err := d.Content.SaveEmbedding(ctx, input, space, vector)
	if !errors.Is(err, content.ErrConflict) {
		return artifact, vector, err
	}
	_, stored, loadErr := d.Content.LoadEmbedding(ctx, org, input.DerivationID)
	if loadErr != nil || len(stored) != len(vector) {
		return artifact, vector, err
	}
	if artifact, err = d.Content.SaveEmbedding(ctx, input, space, stored); err != nil {
		return artifact, vector, err
	}
	return artifact, stored, nil
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
			space, _ := d.descriptor.VectorSpace(key)
			vector := segments[i].Vectors[key]
			artifact, vector, err := d.saveOrAdopt(ctx, org, content.EmbeddingInput(org, corpusID, v, seg, p, space, d.descriptor.Producer), space, vector)
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
	seg, err := content.PluginSegmentation(org, v, d.descriptor.Recipe, d.descriptor.Provenance, inputs)
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
			space, _ := d.descriptor.VectorSpace(key)
			input := content.EmbeddingInput(org, corpusID, v, seg, p, space, d.descriptor.Producer)
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
