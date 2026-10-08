package content

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// ObservabilityRead is the action that reads the operator views of document
// activity (THE-794) and of plugin and search rollups (THE-795).
const ObservabilityRead = "observability:read"

// Steps are when each processing step of a Record Version finished. Each is
// written once, by the transaction that commits the step, and is nil until
// then; Versions accepted before step times were recorded keep nil.
// Withdrawn is the Record's withdrawal, shared by all its Versions.
type Steps struct {
	Accepted, Materialized, Segmented, RetrievalReady, Enriched, Evaluated, Quarantined, Withdrawn *time.Time
}

// Step names, in pipeline order.
const (
	StepAccepted       = "accepted"
	StepMaterialized   = "materialized"
	StepSegmented      = "segmented"
	StepRetrievalReady = "retrieval_ready"
	StepEnriched       = "enriched"
	StepEvaluated      = "evaluated"
	StepQuarantined    = "quarantined"
	StepWithdrawn      = "withdrawn"
)

// Activity states of a Version in the operator views beyond its Version
// Availability: received before it is materialized, withdrawn once its
// Record is.
const (
	ActivityReceived  = "received"
	ActivityWithdrawn = "withdrawn"
)

// Whether alert evaluation applies to a Version: it was evaluated, an
// evaluation of it is pending, or an enabled Subscription covers its Corpus
// now. A Subscription created after the Version became searchable never
// evaluates it, yet makes it applicable.
const (
	EvaluationApplicable    = "applicable"
	EvaluationNotApplicable = "not_applicable"
)

// PluginRef names the plugin version that ran a step.
type PluginRef struct {
	ID, Version string
}

// Activity is one Record Version as the operator views follow it.
type Activity struct {
	VersionID, RecordID string
	Source              Source
	// Title is the command's inline title Part, truncated; empty when none.
	Title   string
	State   string
	Current bool
	Steps   Steps
	// Evaluation is EvaluationApplicable or EvaluationNotApplicable.
	Evaluation string
	// Normalizer ran the materialized step; Ingestion cut the segments the
	// Corpus serves. Filled on single reads only; nil when unknown.
	Normalizer, Ingestion *PluginRef
}

// ActivityCursor is the keyset position after which a page continues.
type ActivityCursor struct {
	AcceptedAt time.Time
	VersionID  string
}

// ActivityStore reads document activity.
type ActivityStore interface {
	LatestActivity(ctx context.Context, org string, after *ActivityCursor, limit int) ([]Activity, error)
	// VersionActivity is corpus.ErrNotFound for an unknown Version.
	VersionActivity(ctx context.Context, org, versionID string) (Activity, error)
}

// Activities authorizes the operator reads of document activity.
type Activities struct {
	Store ActivityStore
}

// Latest lists the Organization's most recently accepted Versions, newest
// first. It spans every Corpus, so it needs a key for all Corpora: a page
// filtered to some Corpora could not stay bounded.
func (a Activities) Latest(ctx context.Context, scope corpus.Scope, after *ActivityCursor, limit int, prepare ...func() (*ActivityCursor, int, error)) ([]Activity, error) {
	if err := scope.Require(corpus.ActionActivityLatest, func() (corpus.Action, error) {
		if a.Store == nil {
			return "", corpus.ErrNotFound
		}
		for _, load := range prepare {
			var err error
			after, limit, err = load()
			if err != nil {
				return "", err
			}
		}
		return corpus.ActionActivityLatest, nil
	}); err != nil {
		return nil, err
	}

	return a.Store.LatestActivity(ctx, scope.Organization, after, limit)
}

// Version reads one Version's activity; a Version outside the scope's
// Corpora is corpus.ErrNotFound.
func (a Activities) Version(ctx context.Context, scope corpus.Scope, versionID string) (Activity, error) {
	if err := scope.Require(corpus.ActionActivityVersion); err != nil {
		return Activity{}, err
	}
	if a.Store == nil {
		return Activity{}, corpus.ErrNotFound
	}
	out, err := a.Store.VersionActivity(ctx, scope.Organization, versionID)
	if err == nil && !scope.Contains(out.Source.CorpusID) {
		return Activity{}, corpus.ErrNotFound
	}
	return out, err
}

// TimelineStep is one finished step: when it finished and, when its
// predecessor finished too, how long it took since then.
type TimelineStep struct {
	Step string
	At   time.Time
	// Since is the step the duration is measured from; empty when none.
	Since    string
	Duration time.Duration
	Plugin   *PluginRef
}

// Timeline orders a Version's finished steps by time. Each pipeline step is
// timed from the step that causes it: vectors and alert evaluation both
// follow the Version becoming searchable. A quarantine is timed from the
// latest pipeline step finished before it; a withdrawal is an external
// command and has no duration.
func Timeline(a Activity) []TimelineStep {
	s := a.Steps
	type entry struct {
		step   string
		at     *time.Time
		since  string
		plugin *PluginRef
	}
	entries := []entry{
		{StepAccepted, s.Accepted, "", nil},
		{StepMaterialized, s.Materialized, StepAccepted, a.Normalizer},
		{StepSegmented, s.Segmented, StepMaterialized, a.Ingestion},
		{StepRetrievalReady, s.RetrievalReady, StepSegmented, a.Ingestion},
		{StepEnriched, s.Enriched, StepRetrievalReady, a.Ingestion},
		{StepEvaluated, s.Evaluated, StepRetrievalReady, nil},
		{StepQuarantined, s.Quarantined, "", nil},
		{StepWithdrawn, s.Withdrawn, "", nil},
	}
	at := map[string]*time.Time{}
	for _, e := range entries {
		at[e.step] = e.at
	}
	if s.Quarantined != nil {
		for _, step := range []string{StepSegmented, StepMaterialized, StepAccepted} {
			if t := at[step]; t != nil && !t.After(*s.Quarantined) {
				entries[6].since = step
				break
			}
		}
	}
	out := []TimelineStep{}
	for _, e := range entries {
		if e.at == nil {
			continue
		}
		step := TimelineStep{Step: e.step, At: *e.at, Plugin: e.plugin}
		// A cause dated after its step, possible only for Versions whose
		// earlier steps predate step times, gives no duration.
		if from := at[e.since]; e.since != "" && from != nil && !from.After(*e.at) {
			step.Since, step.Duration = e.since, e.at.Sub(*from)
		}
		out = append(out, step)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// maxTitleRunes bounds the title kept for the operator views.
const maxTitleRunes = 200

// Title is the first inline text Part with the role title, trimmed and
// truncated for display; empty when the command has none.
func Title(c Command) string {
	if c.Manifest == nil {
		return ""
	}
	for _, p := range c.Manifest.Parts {
		if p.Role != "title" || p.Content.Kind != "text" {
			continue
		}
		title := []rune(strings.TrimSpace(p.Content.Text))
		if len(title) > maxTitleRunes {
			title = title[:maxTitleRunes]
		}
		return string(title)
	}
	return ""
}
