package content

import (
	"context"
	"encoding/json"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// SegmentInput is one segment an ingestion plugin cut from a Version: Unicode
// code point offsets into a text Part, and the lexical text and provenance
// the plugin returned for it.
type SegmentInput struct {
	PartKey     string
	Start, End  int
	LexicalText string
	Provenance  json.RawMessage
}

// StoredSegmentation is a segmentation as PostgreSQL keeps it: its identity,
// digest and provenance, and each segment's id, offsets and derivation.
type StoredSegmentation struct {
	ID, Digest string
	Provenance json.RawMessage
	Segments   []StoredSegment
}

// StoredSegment is one stored segment.
type StoredSegment struct {
	ID, PartKey string
	Start, End  int
	Derivation  SegmentDerivation
}

// SegmentationStore reads a stored segmentation of a Version by recipe;
// corpus.ErrNotFound when there is none.
type SegmentationStore interface {
	StoredSegmentation(ctx context.Context, org, versionID, recipe string) (StoredSegmentation, error)
}

// PluginSegmentation builds the Segmentation of an ingestion plugin's
// segments. Segments are numbered in order; each carries the Version's title
// when it has exactly one text Part with the role title, as the built-in
// windows do, so the projection's title field is the same for every path.
// The recipe names the plugin and its version, so another plugin, or another
// version of it, makes another Segmentation with other segment ids.
func PluginSegmentation(org string, v Version, recipe string, provenance json.RawMessage, in []SegmentInput) (Segmentation, error) {
	out := Segmentation{ID: StableID("segmentation", org, v.ID, recipe), VersionID: v.ID, Recipe: recipe, Provenance: canonical(provenance)}
	parts := map[string]string{}
	title, titleKey, titles := "", "", 0
	for _, p := range v.Manifest.Parts {
		if p.Content.Kind != "text" {
			continue
		}
		parts[p.Key] = p.Content.Text
		if p.Role == "title" {
			title, titleKey = p.Content.Text, p.Key
			titles++
		}
	}
	if titles != 1 {
		title, titleKey = "", ""
	}
	empty := Hash(nil)
	for i, s := range in {
		text, ok := parts[s.PartKey]
		runes := []rune(text)
		if !ok || s.Start < 0 || s.End < s.Start || s.End > len(runes) {
			return out, ErrInvalid
		}
		d := SegmentDerivation{Ordinal: i, UTF8Start: len(string(runes[:s.Start])), UTF8End: len(string(runes[:s.End])),
			NormalizedSHA256: Hash([]byte(text)), ModelInputSHA256: empty, LexicalText: s.LexicalText, Provenance: canonical(s.Provenance)}
		segment := Segment{PartKey: s.PartKey, Text: string(runes[s.Start:s.End]), Start: s.Start, End: s.End, Title: title, TitleKey: titleKey, Derivation: d}
		segment.ID = SegmentID(org, out.ID, segment)
		out.Segments = append(out.Segments, segment)
	}
	if len(out.Segments) == 0 {
		return out, ErrInvalid
	}
	return out, nil
}

// PluginSegmentationOf reads back a stored plugin segmentation and rebuilds
// it from the Version's canonical text. It is ErrConflict when the rebuilt
// segments or digest differ from what was stored, and corpus.ErrNotFound when
// the Version has no segmentation of that recipe.
func (s Service) PluginSegmentationOf(ctx context.Context, org string, v Version, recipe string) (Segmentation, error) {
	store, ok := s.Baseline.(SegmentationStore)
	if !ok {
		return Segmentation{}, corpus.ErrNotFound
	}
	stored, err := store.StoredSegmentation(ctx, org, v.ID, recipe)
	if err != nil {
		return Segmentation{}, err
	}
	in := make([]SegmentInput, len(stored.Segments))
	for i, p := range stored.Segments {
		in[i] = SegmentInput{PartKey: p.PartKey, Start: p.Start, End: p.End, LexicalText: p.Derivation.LexicalText, Provenance: p.Derivation.Provenance}
	}
	seg, err := PluginSegmentation(org, v, recipe, stored.Provenance, in)
	if err != nil {
		return seg, ErrConflict
	}
	for i := range seg.Segments {
		if seg.Segments[i].ID != stored.Segments[i].ID {
			return seg, ErrConflict
		}
	}
	if seg.ID != stored.ID || SegmentationDigest(seg) != stored.Digest {
		return seg, ErrConflict
	}
	return seg, nil
}

// canonical re-encodes a JSON value with sorted keys and no spaces, so a
// value read back from PostgreSQL (jsonb reorders keys) hashes the same.
func canonical(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	out, _ := json.Marshal(v)
	return out
}
