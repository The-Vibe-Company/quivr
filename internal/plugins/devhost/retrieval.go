package devhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// FixtureSpaceDimensions is the dimensions of every space a retrieval fixture
// offers.
const FixtureSpaceDimensions = 8

// defaultFixtureSpace is the space a retrieval fixture offers when it names
// none.
const defaultFixtureSpace = "fixture.served@1"

// IsRetrievalFixture reports whether a fixture file is a retrieval fixture
// (contracts/plugins/v0/retrieval-fixture.schema.json): it has a top-level
// retrieval property.
func IsRetrievalFixture(raw []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	_, ok := probe["retrieval"]
	return ok
}

// FixtureCandidate is one segment of a retrieval fixture's catalogue.
type FixtureCandidate struct {
	SegmentID       string   `json:"segment_id"`
	RecordID        string   `json:"record_id"`
	SourceNamespace string   `json:"source_namespace,omitempty"`
	Text            string   `json:"text"`
	Score           *float64 `json:"score,omitempty"`
	Explanation     string   `json:"explanation,omitempty"`
}

type retrievalFixture struct {
	Retrieval struct {
		Query         string              `json:"query"`
		Mode          string              `json:"mode,omitempty"`
		Limit         int                 `json:"limit,omitempty"`
		Configuration json.RawMessage     `json:"configuration,omitempty"`
		Profiles      []string            `json:"profiles,omitempty"`
		Spaces        []string            `json:"spaces,omitempty"`
		Candidates    []FixtureCandidate  `json:"candidates"`
		Order         map[string][]string `json:"order,omitempty"`
		Expect        *struct {
			Top []string `json:"top"`
		} `json:"expect,omitempty"`
	} `json:"retrieval"`
}

// RetrievalRun is one retrieval fixture ready to drive: the first request of
// a search per profile, and the catalogue the runner serves candidates from.
type RetrievalRun struct {
	Profiles []string
	// Top is the ranking's expected first hits; nil expects nothing.
	Top        []string
	first      plugins.SearchRequest
	candidates []FixtureCandidate
	order      map[string][]string
	short      string
}

// BuildRetrievalRun turns retrieval fixture bytes into a development search.
// Ids derive from the first 16 hex digits of the SHA-256 of the fixture bytes
// (dev-invocation-…), with Corpus dev-corpus and Organization
// dev-organization. Profiles default to every declared profile; spaces to one
// served space, fixture.served@1, of FixtureSpaceDimensions dimensions.
// Issues report an invalid fixture, a configuration the manifest rejects, an
// undeclared profile or a duplicate segment.
func BuildRetrievalRun(raw []byte, m *plugins.Manifest) (*RetrievalRun, []plugins.Issue) {
	if issues := plugins.ValidateDocument("retrieval-fixture.schema.json", raw); len(issues) > 0 {
		return nil, issues
	}
	if m == nil || m.Contributions.Retrieval == nil {
		return nil, []plugins.Issue{{Code: plugins.CodeInvalidManifest, Path: "/contributions/retrieval",
			Message: "this is a retrieval fixture, but the manifest declares no retrieval Contribution"}}
	}
	var f retrievalFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, []plugins.Issue{{Code: plugins.CodeSchema, Message: err.Error()}}
	}
	in := f.Retrieval
	config := in.Configuration
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	issues := plugins.ValidateConfiguration(m, config)
	profiles := in.Profiles
	if len(profiles) == 0 {
		profiles = m.Contributions.Retrieval.ProfileNames()
	}
	for i, p := range profiles {
		if _, ok := m.Contributions.Retrieval.Profiles[p]; !ok {
			issues = append(issues, plugins.Issue{Code: plugins.CodeSchema, Path: fmt.Sprintf("/retrieval/profiles/%d", i),
				Message: fmt.Sprintf("profile %q is not declared by the manifest (declared: %v)", p, m.Contributions.Retrieval.ProfileNames())})
		}
	}
	seen := map[string]bool{}
	for i, c := range in.Candidates {
		if seen[c.SegmentID] {
			issues = append(issues, plugins.Issue{Code: plugins.CodeSchema, Path: fmt.Sprintf("/retrieval/candidates/%d/segment_id", i),
				Message: fmt.Sprintf("segment %q is listed twice", c.SegmentID)})
		}
		seen[c.SegmentID] = true
	}
	for primitive, ids := range in.Order {
		for i, id := range ids {
			if !seen[id] {
				issues = append(issues, plugins.Issue{Code: plugins.CodeSchema, Path: fmt.Sprintf("/retrieval/order/%s/%d", primitive, i),
					Message: fmt.Sprintf("segment %q is not in the candidates", id)})
			}
		}
	}
	if len(issues) > 0 {
		return nil, issues
	}
	ids := in.Spaces
	if len(ids) == 0 {
		ids = []string{defaultFixtureSpace}
	}
	total := int64(len(in.Candidates))
	spaces := make([]plugins.SearchSpace, len(ids))
	for i, id := range ids {
		role := "evaluation"
		if i == 0 {
			role = "served"
		}
		spaces[i] = plugins.SearchSpace{ID: id, Owner: plugins.SpaceOwner{Kind: "engine"}, Model: "fixture", Dimensions: FixtureSpaceDimensions, Metric: "cosine",
			Indexes: []string{"text"}, QueryModalities: []string{"text"}, Role: role, Coverage: plugins.SpaceCoverage{Segments: total, Total: total}}
	}
	mode, limit := in.Mode, in.Limit
	if mode == "" {
		mode = "hybrid"
	}
	if limit == 0 {
		limit = 10
	}
	sum := sha256.Sum256(raw)
	short := hex.EncodeToString(sum[:])[:16]
	run := &RetrievalRun{Profiles: profiles, candidates: in.Candidates, order: in.Order, short: short,
		first: plugins.SearchRequest{OrganizationID: "dev-organization", Configuration: config, Query: plugins.SearchQuery{Text: in.Query, Mode: mode},
			Limit: limit, Scope: plugins.SearchScope{CorpusIDs: []string{"dev-corpus"}}, Spaces: spaces}}
	if in.Expect != nil {
		run.Top = in.Expect.Top
	}
	return run, nil
}

// Request is the round 1 request of a search under profile; suffix makes the
// invocation id unique.
func (r *RetrievalRun) Request(profile, suffix string) plugins.SearchRequest {
	q := r.first
	q.Profile = profile
	q.InvocationID = "dev-invocation-" + r.short + "-" + suffix
	return q
}

// Serve answers one candidate request from the catalogue, offline: the
// fixture's order for the primitive, or else the segments that share words
// with the query (every segment for near_vector), most shared words first,
// ties in catalogue order. Filters, grouping by Record and k apply.
func (r *RetrievalRun) Serve(c plugins.CandidateRequest) []plugins.Candidate {
	byID := map[string]FixtureCandidate{}
	for _, f := range r.candidates {
		byID[f.SegmentID] = f
	}
	var ranked []FixtureCandidate
	if c.Primitive == plugins.PrimitiveProfile && c.Profile != nil {
		if c.Profile.Query != "" {
			c.QueryText = c.Profile.Query
		} else {
			c.QueryText = r.first.Query.Text
		}
	}
	if ids, ok := r.order[c.Primitive]; ok {
		for _, id := range ids {
			ranked = append(ranked, byID[id])
		}
	} else {
		words := wordSet(c.QueryText)
		overlap := map[string]int{}
		for _, f := range r.candidates {
			for w := range wordSet(f.Text) {
				if words[w] {
					overlap[f.SegmentID]++
				}
			}
			if overlap[f.SegmentID] > 0 || c.Primitive == plugins.PrimitiveNearVector {
				ranked = append(ranked, f)
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return overlap[ranked[i].SegmentID] > overlap[ranked[j].SegmentID] })
	}
	out := []plugins.Candidate{}
	records := map[string]bool{}
	for _, f := range ranked {
		if c.Filter != nil && len(c.Filter.SourceNamespaces) > 0 && !slices.Contains(c.Filter.SourceNamespaces, f.SourceNamespace) {
			continue
		}
		if c.GroupBy == plugins.GroupByRecord {
			if records[f.RecordID] {
				continue
			}
			records[f.RecordID] = true
		}
		score := 1 / float64(len(out)+1)
		explanation := ""
		if c.Primitive == plugins.PrimitiveProfile {
			if f.Score != nil {
				score = *f.Score
			}
			explanation = f.Explanation
		}
		out = append(out, plugins.Candidate{SegmentID: f.SegmentID, RecordID: f.RecordID, VersionID: "dev-version-" + f.RecordID, PartKey: "body",
			Text: f.Text, End: utf8.RuneCountInString(f.Text), Score: score, Explanation: explanation})
		if len(out) == c.Limit() {
			break
		}
	}
	return out
}

func wordSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		out[w] = true
	}
	return out
}

// SearchOutcome is what one driven search produced.
type SearchOutcome struct {
	Ranking []plugins.RankedHit
	Rounds  int
	Usage   plugins.SearchUsage
	// Result is the last round's result: an error envelope or issues when the
	// search failed.
	Result *Result
	// Issues are the engine's refusals of an answer, empty on success.
	Issues []plugins.Issue
}

// DriveSearch runs a whole search against a plugin as the engine does: it
// posts each round, judges the answer with plugins.RetrievalSession, serves
// requested candidates with serve, and stops at the ranking, a refusal or an
// error. ctx carries the profile's deadline.
func DriveSearch(ctx context.Context, baseURL string, m *plugins.Manifest, first plugins.SearchRequest, serve func(plugins.CandidateRequest) []plugins.Candidate) (*SearchOutcome, error) {
	session := plugins.NewRetrievalSession(m, first)
	out := &SearchOutcome{}
	for {
		request := session.Request()
		if m.SupportsSearchBudget() {
			remaining := 0
			if deadline, ok := ctx.Deadline(); ok {
				remaining = int(max(0, time.Until(deadline).Milliseconds()))
			}
			request.Budget = &plugins.SearchBudget{RemainingTimeMS: remaining, RemainingCostCents: max(0, m.Contributions.Retrieval.Profiles[request.Profile].MaxCostCents-session.Usage().CostCents)}
		}
		body, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		result, err := InvokeSearch(ctx, baseURL, body, plugins.RetrievalMaxResponseBytes(m), func([]byte) []plugins.Issue { return nil })
		out.Rounds, out.Result = session.Round(), result
		if err != nil {
			return out, err
		}
		if result.Error != nil || len(result.Issues) > 0 {
			return out, nil
		}
		answer, issues := session.Judge(result.Body)
		if len(issues) > 0 {
			for i := range issues {
				issues[i].Message = fmt.Sprintf("round %d: %s", session.Round(), issues[i].Message)
			}
			out.Issues = issues
			return out, nil
		}
		if answer.Ranking != nil {
			out.Ranking, out.Usage = answer.Ranking.Hits, session.Usage()
			if issue := session.Budget(); issue != nil {
				out.Issues = []plugins.Issue{*issue}
			}
			return out, nil
		}
		served := make([][]plugins.Candidate, len(answer.Requests))
		for i, c := range answer.Requests {
			served[i] = serve(c)
		}
		session.Serve(fmt.Sprintf("%s-round-%d", first.InvocationID, session.Round()+1), answer.Requests, served)
	}
}
