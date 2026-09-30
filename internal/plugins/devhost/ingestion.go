package devhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// CodeUnexpectedSegments is a segment_and_embed answer whose segments or
// lexical text differ from what an ingestion fixture expects.
const CodeUnexpectedSegments = "unexpected_segments"

// defaultQueryRunes bounds the query an ingestion fixture without queries
// takes from its first non-empty Part.
const defaultQueryRunes = 200

// IsIngestionFixture reports whether a fixture file is an ingestion fixture
// (contracts/plugins/v0/ingestion-fixture.schema.json): it has a top-level
// ingestion property.
func IsIngestionFixture(raw []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	_, ok := probe["ingestion"]
	return ok
}

// ExpectedSegment is one segment an ingestion fixture expects.
type ExpectedSegment struct {
	PartKey string `json:"part_key"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
}

// IngestionExpect is what an ingestion fixture expects.
type IngestionExpect struct {
	Segments    []ExpectedSegment `json:"segments,omitempty"`
	LexicalText *bool             `json:"lexical_text,omitempty"`
}

type ingestionFixture struct {
	Ingestion struct {
		Parts         []plugins.IngestionPart `json:"parts"`
		Language      string                  `json:"language,omitempty"`
		Configuration json.RawMessage         `json:"configuration,omitempty"`
		Spaces        []string                `json:"spaces,omitempty"`
		Queries       []string                `json:"queries,omitempty"`
		Expect        *IngestionExpect        `json:"expect,omitempty"`
	} `json:"ingestion"`
}

type ingestionVersion struct {
	CorpusID        string `json:"corpus_id"`
	RecordID        string `json:"record_id"`
	RecordVersionID string `json:"record_version_id"`
}

type segmentAndEmbedRequest struct {
	InvocationID   string                  `json:"invocation_id"`
	IdempotencyKey string                  `json:"idempotency_key"`
	Contribution   string                  `json:"contribution"`
	OrganizationID string                  `json:"organization_id"`
	Configuration  json.RawMessage         `json:"configuration"`
	Version        ingestionVersion        `json:"version"`
	Language       string                  `json:"language,omitempty"`
	Parts          []plugins.IngestionPart `json:"parts"`
	Spaces         []string                `json:"spaces"`
}

type embedQueryRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Space          string          `json:"space"`
	Query          struct {
		Modality string `json:"modality"`
		Text     string `json:"text"`
	} `json:"query"`
}

// IngestionRun is the development segment_and_embed request of one ingestion
// fixture, the spaces it asks for and the queries to encode in each.
type IngestionRun struct {
	Request []byte
	View    plugins.IngestionRequestView
	Queries []string
	Expect  *IngestionExpect
	short   string
	digest  string
	config  json.RawMessage
}

// BuildIngestionRun turns ingestion fixture bytes into a development request.
// The ids derive from the first 16 hex digits of the SHA-256 of the fixture
// bytes (dev-invocation-…, dev-record-…, dev-version-…), with Corpus
// dev-corpus, Organization dev-organization and idempotency key
// dev:<sha256>. Spaces default to every declared space; queries default to
// the first 200 code points of the first non-empty Part. Issues report an
// invalid fixture, a configuration the manifest rejects, an undeclared space
// or duplicate Part keys.
func BuildIngestionRun(raw []byte, m *plugins.Manifest) (*IngestionRun, []plugins.Issue) {
	if issues := plugins.ValidateDocument("ingestion-fixture.schema.json", raw); len(issues) > 0 {
		return nil, issues
	}
	if m == nil || m.Contributions.Ingestion == nil {
		return nil, []plugins.Issue{{Code: plugins.CodeInvalidManifest, Path: "/contributions/ingestion",
			Message: "this is an ingestion fixture, but the manifest declares no ingestion Contribution"}}
	}
	var f ingestionFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, []plugins.Issue{{Code: plugins.CodeSchema, Message: err.Error()}}
	}
	in := f.Ingestion
	config := in.Configuration
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	issues := plugins.ValidateConfiguration(m, config)
	seen := map[string]bool{}
	for i, p := range in.Parts {
		if seen[p.Key] {
			issues = append(issues, plugins.Issue{Code: plugins.CodeSchema, Path: fmt.Sprintf("/ingestion/parts/%d/key", i),
				Message: fmt.Sprintf("duplicate Part key %q; the Parts of a Record Version have unique keys", p.Key)})
		}
		seen[p.Key] = true
	}
	spaces := in.Spaces
	if len(spaces) == 0 {
		for id := range m.Contributions.Ingestion.Spaces {
			spaces = append(spaces, id)
		}
		sort.Strings(spaces)
	}
	for i, id := range spaces {
		if _, ok := m.Contributions.Ingestion.Spaces[id]; !ok {
			issues = append(issues, plugins.Issue{Code: plugins.CodeSchema, Path: fmt.Sprintf("/ingestion/spaces/%d", i),
				Message: fmt.Sprintf("space %q is not declared by the manifest", id)})
		}
	}
	if len(issues) > 0 {
		return nil, issues
	}
	queries := in.Queries
	if len(queries) == 0 {
		for _, p := range in.Parts {
			if runes := []rune(p.Text); len(runes) > 0 {
				queries = []string{string(runes[:min(len(runes), defaultQueryRunes)])}
				break
			}
		}
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	short := digest[:16]
	body, err := json.Marshal(segmentAndEmbedRequest{
		InvocationID: "dev-invocation-" + short, IdempotencyKey: "dev:" + digest, Contribution: "ingestion",
		OrganizationID: "dev-organization", Configuration: config,
		Version:  ingestionVersion{CorpusID: "dev-corpus", RecordID: "dev-record-" + short, RecordVersionID: "dev-version-" + short},
		Language: in.Language, Parts: in.Parts, Spaces: spaces,
	})
	if err != nil {
		return nil, []plugins.Issue{{Code: plugins.CodeSchema, Message: err.Error()}}
	}
	if issues := plugins.ValidateDocument("ingestion-segment-and-embed-request.schema.json", body); len(issues) > 0 {
		return nil, issues
	}
	return &IngestionRun{Request: body, View: plugins.IngestionRequestView{Parts: in.Parts, Spaces: spaces}, Queries: queries, Expect: in.Expect, short: short, digest: digest, config: config}, nil
}

// WithInvocation returns the segment_and_embed request under another
// invocation id and the same idempotency key, as a retry sends it.
func (r *IngestionRun) WithInvocation(suffix string) []byte {
	var request map[string]any
	_ = json.Unmarshal(r.Request, &request)
	request["invocation_id"] = "dev-invocation-" + r.short + "-" + suffix
	body, _ := json.Marshal(request)
	return body
}

// QueryRequest is the development embed_query request of one query in one
// space; suffix makes the invocation id unique.
func (r *IngestionRun) QueryRequest(space, text, suffix string) []byte {
	request := embedQueryRequest{InvocationID: "dev-invocation-" + r.short + "-query-" + suffix, Contribution: "ingestion",
		OrganizationID: "dev-organization", Configuration: r.config, Space: space}
	request.Query.Modality = "text"
	request.Query.Text = text
	body, _ := json.Marshal(request)
	return body
}

// InvokeSegmentAndEmbed posts a segment_and_embed request and judges the
// answer like InvokeNormalizerWith; check is normally a closure over
// plugins.CheckSegmentAndEmbedOutput.
func InvokeSegmentAndEmbed(ctx context.Context, baseURL string, request []byte, maxResponseBytes int, check func(body []byte) []plugins.Issue) (*Result, error) {
	return invoke(ctx, baseURL, plugins.SegmentAndEmbedRoute, request, maxResponseBytes, check)
}

// InvokeEmbedQuery posts an embed_query request; check is normally a closure
// over plugins.CheckEmbedQueryOutput.
func InvokeEmbedQuery(ctx context.Context, baseURL string, request []byte, check func(body []byte) []plugins.Issue) (*Result, error) {
	return invoke(ctx, baseURL, plugins.EmbedQueryRoute, request, plugins.EmbedQueryMaxResponseBytes, check)
}

// SegmentExpectationIssues compares a valid answer with what a fixture
// expects: the exact segments in order, and lexical text on every segment or
// on none.
func SegmentExpectationIssues(body []byte, expect *IngestionExpect) []plugins.Issue {
	if expect == nil {
		return nil
	}
	answer, err := plugins.DecodeSegmentAndEmbed(body)
	if err != nil {
		return nil
	}
	var issues []plugins.Issue
	if expect.Segments != nil {
		got := make([]ExpectedSegment, len(answer.Segments))
		for i, s := range answer.Segments {
			got[i] = ExpectedSegment{PartKey: s.PartKey, Start: s.Start, End: s.End}
		}
		if fmt.Sprint(got) != fmt.Sprint(expect.Segments) {
			issues = append(issues, plugins.Issue{Code: CodeUnexpectedSegments, Path: "/segments",
				Message: fmt.Sprintf("segments %v; the fixture expects %v", got, expect.Segments)})
		}
	}
	if expect.LexicalText != nil {
		for i, s := range answer.Segments {
			if (s.LexicalText != "") != *expect.LexicalText {
				issues = append(issues, plugins.Issue{Code: CodeUnexpectedSegments, Path: fmt.Sprintf("/segments/%d/lexical_text", i),
					Message: fmt.Sprintf("the fixture expects lexical text on %s segment", map[bool]string{true: "every", false: "no"}[*expect.LexicalText])})
				break
			}
		}
	}
	return issues
}
