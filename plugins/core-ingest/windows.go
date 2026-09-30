package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The token-window segmentation that the engine ran before THE-777, moved here
// unchanged: body Parts are cut into windows of at most body_tokens tokens that
// prefer paragraph, line and sentence boundaries and overlap by overlap_tokens;
// each segment's model input is "passage: " followed by the title (capped at
// title_tokens) and the trimmed window. testdata/golden.json holds what the
// engine produced; parity_test.go holds this code to it.

//go:embed profile.json
var profile []byte

type profileParameters struct {
	BodyTokens              int `json:"body_tokens"`
	OverlapTokens           int `json:"overlap_tokens"`
	OverlapBacktrack        int `json:"overlap_backtrack"`
	MinimumBoundaryTokens   int `json:"minimum_boundary_tokens"`
	TitleTokens             int `json:"title_tokens"`
	QueryTokens             int `json:"query_tokens"`
	ModelTokens             int `json:"model_tokens"`
	MaxSourceBytes          int `json:"max_source_bytes"`
	MaxSegments             int `json:"max_segments"`
	MaxExcerptCodepoints    int `json:"max_excerpt_codepoints"`
	MaxModelBatchBytes      int `json:"max_model_batch_bytes"`
	MaxSerializedBatchBytes int `json:"max_serialized_batch_bytes"`
	MaxParts                int `json:"max_parts"`
	MaxQueryCodepoints      int `json:"max_query_codepoints"`
}

// Parameters are the pinned recipe values of profile.json.
var Parameters = func() profileParameters {
	var p profileParameters
	if err := json.Unmarshal(profile, &p); err != nil {
		panic(err)
	}
	return p
}()

// ErrUnsupported is content the recipe can never segment: a limit is exceeded
// or the text is invalid. The plugin refuses it with a terminal error.
var ErrUnsupported = errors.New("segmentation_limit")

// errInvalidQuery is a query the model cannot take without truncation.
var errInvalidQuery = errors.New("invalid query")

func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

// Part is one text Part of a Record Version.
type Part struct {
	Key, Role, Text string
}

// Derivation records how one segment was made; it is the segment's provenance.
type Derivation struct {
	Ordinal          int    `json:"ordinal"`
	UTF8Start        int    `json:"utf8_start"`
	UTF8End          int    `json:"utf8_end"`
	TokenStart       int    `json:"token_start"`
	TokenEnd         int    `json:"token_end"`
	Overlap          int    `json:"overlap_tokens"`
	HardStart        bool   `json:"hard_start"`
	HardEnd          bool   `json:"hard_end"`
	NormalizedSHA256 string `json:"normalized_content_sha256"`
	TitleFullSHA256  string `json:"full_title_sha256,omitempty"`
	TitleUsedSHA256  string `json:"title_used_sha256,omitempty"`
	TitleTokens      int    `json:"title_tokens"`
	TitleTruncated   bool   `json:"title_truncated"`
	ModelInput       string `json:"-"`
	ModelInputSHA256 string `json:"model_input_sha256"`
	ModelTokens      int    `json:"model_tokens"`
}

// Window is one segment: code point offsets into its Part and its derivation.
type Window struct {
	PartKey    string
	Start, End int
	Derivation Derivation
}

type TokenInput struct {
	Text    string `json:"text"`
	Special bool   `json:"special"`
}
type Encoding struct {
	Tokens  int      `json:"tokens"`
	Offsets [][2]int `json:"offsets"`
}
type Tokenizer interface {
	Encode(context.Context, []TokenInput) ([]Encoding, error)
}
type TokenWindows struct{ Tokenizer Tokenizer }

func (p TokenWindows) NormalizeQuery(ctx context.Context, q string) (string, error) {
	if !validText(q) || utf8.RuneCountInString(q) > Parameters.MaxQueryCodepoints {
		return "", errInvalidQuery
	}
	q = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(q, "\r\n", "\n"), "\r", "\n"))
	if q == "" {
		return "", errInvalidQuery
	}
	e, err := p.Tokenizer.Encode(ctx, []TokenInput{{Text: q}, {Text: "query: " + q, Special: true}})
	if err != nil {
		return "", err
	}
	if e[0].Tokens > Parameters.QueryTokens || e[1].Tokens > Parameters.ModelTokens {
		return "", errInvalidQuery
	}
	return q, nil
}

func (p TokenWindows) Process(ctx context.Context, parts []Part) ([]Window, error) {
	var out []Window
	// Only explicit title/body text Parts contribute normalized text. Blob Parts
	// and other roles stay in the immutable Manifest without extraction.
	type source struct {
		part  Part
		input TokenInput
	}
	sources := []source{}
	total := 0
	titleSlot := -1
	for _, part := range parts {
		if part.Role != "title" && part.Role != "body" {
			continue
		}
		if !validText(part.Text) {
			return out, ErrUnsupported
		}
		total += len(part.Text)
		if part.Role == "title" {
			if titleSlot >= 0 {
				return out, ErrUnsupported
			}
			titleSlot = len(sources)
		}
		inputText := part.Text
		if part.Role == "title" {
			inputText = strings.TrimSpace(inputText)
		}
		sources = append(sources, source{part: part, input: TokenInput{Text: inputText}})
	}
	if total > Parameters.MaxSourceBytes || len(sources) == 0 || len(sources) > Parameters.MaxParts {
		return out, ErrUnsupported
	}
	inputs := make([]TokenInput, len(sources))
	for i, s := range sources {
		inputs[i] = s.input
	}
	enc, err := p.Tokenizer.Encode(ctx, inputs)
	if err != nil {
		return out, err
	}
	for i, e := range enc {
		if !validOffsets([]rune(inputs[i].Text), e) {
			return out, ErrUnsupported
		}
	}
	title, titleUsed := "", ""
	titleTokens := 0
	truncated := false
	if titleSlot >= 0 {
		part := sources[titleSlot].part
		title = part.Text
		titleUsed = strings.TrimSpace(title)
		titleTokens = enc[titleSlot].Tokens
		if titleTokens > Parameters.TitleTokens {
			cut := Parameters.TitleTokens
			offsets := enc[titleSlot].Offsets
			for cut > 0 && offsets[cut-1][1] > offsets[cut][0] {
				cut--
			}
			if cut == 0 {
				return out, ErrUnsupported
			}
			titleUsed = strings.TrimSpace(string([]rune(titleUsed)[:offsets[cut-1][1]]))
			titleTokens = cut
			truncated = true
		}
	}
	for i, s := range sources {
		if s.part.Role == "title" {
			continue
		}
		part := s.part
		runes := []rune(part.Text)
		tokens := enc[i]
		spans, err := windows(runes, tokens)
		if err != nil {
			return out, err
		}
		if len(spans) == 0 && title != "" {
			spans = []span{{}}
		}
		for _, w := range spans {
			raw := string(runes[w.start:w.end])
			if utf8.RuneCountInString(raw) > Parameters.MaxExcerptCodepoints {
				return out, ErrUnsupported
			}
			modelInput := "passage: " + strings.TrimSpace(raw)
			if titleUsed != "" {
				modelInput = "passage: " + titleUsed
				if strings.TrimSpace(raw) != "" {
					modelInput += "\n\n" + strings.TrimSpace(raw)
				}
			}
			d := Derivation{Ordinal: len(out), UTF8Start: len(string(runes[:w.start])), UTF8End: len(string(runes[:w.end])), TokenStart: w.first, TokenEnd: w.last, Overlap: w.overlap, HardStart: w.hardStart, HardEnd: w.hardEnd, NormalizedSHA256: hash([]byte(part.Text)), ModelInput: modelInput, ModelInputSHA256: hash([]byte(modelInput)), TitleTokens: titleTokens, TitleTruncated: truncated}
			if title != "" {
				d.TitleFullSHA256 = hash([]byte(title))
				d.TitleUsedSHA256 = hash([]byte(titleUsed))
			}
			out = append(out, Window{PartKey: part.Key, Start: w.start, End: w.end, Derivation: d})
			if len(out) > Parameters.MaxSegments {
				return out, ErrUnsupported
			}
		}
	}
	if len(out) == 0 {
		return out, ErrUnsupported
	}
	modelInputs := make([]TokenInput, len(out))
	for i, s := range out {
		modelInputs[i] = TokenInput{Text: s.Derivation.ModelInput, Special: true}
	}
	if title != "" {
		modelInputs = append(modelInputs, TokenInput{Text: titleUsed})
	}
	batchBytes := 0
	for _, item := range modelInputs {
		batchBytes += len(item.Text)
	}
	if batchBytes > Parameters.MaxModelBatchBytes {
		return out, ErrUnsupported
	}
	counts, err := p.Tokenizer.Encode(ctx, modelInputs)
	if err != nil {
		return out, err
	}
	if title != "" {
		titleTokens = counts[len(out)].Tokens
		if titleTokens > Parameters.TitleTokens {
			return out, ErrUnsupported
		}
	}
	for i, e := range counts[:len(out)] {
		out[i].Derivation.TitleTokens = titleTokens
		if e.Tokens > Parameters.ModelTokens {
			return out, ErrUnsupported
		}
		out[i].Derivation.ModelTokens = e.Tokens
	}
	return out, nil
}
func validOffsets(text []rune, e Encoding) bool {
	if e.Tokens != len(e.Offsets) {
		return false
	}
	previous := 0
	for _, o := range e.Offsets {
		if o[0] < previous || o[1] < o[0] || o[1] > len(text) {
			return false
		}
		previous = o[0]
	}
	return true
}

type span struct {
	start, end, first, last, overlap int
	hardStart, hardEnd               bool
}

func boundary(text []rune, e Encoding, token int) int {
	if token == e.Tokens {
		return len(text)
	}
	end := e.Offsets[token][0]
	for end < len(text) && unicode.IsSpace(text[end]) {
		end++
	}
	return end
}
func category(text []rune, end int) int {
	i := end
	for i > 0 && unicode.IsSpace(text[i-1]) {
		i--
	}
	gap := string(text[i:end])
	gap = strings.ReplaceAll(strings.ReplaceAll(gap, "\r\n", "\n"), "\r", "\n")
	if strings.Count(gap, "\n") >= 2 {
		return 0
	}
	if strings.Contains(gap, "\n") {
		return 1
	}
	last := i - 1
	for last >= 0 && strings.ContainsRune("\"'”’»)]}", text[last]) {
		last--
	}
	if last >= 0 && strings.ContainsRune(".!?…", text[last]) {
		return 2
	}
	if i < end {
		return 3
	}
	return 4
}
func safeStart(text []rune, pos int) bool {
	return pos == 0 || unicode.IsSpace(text[pos-1]) || strings.ContainsRune("([{\"'“‘«", text[pos-1])
}
func windows(text []rune, e Encoding) ([]span, error) {
	if len(text) == 0 {
		return nil, nil
	}
	if e.Tokens == 0 {
		return nil, ErrUnsupported
	}
	spans := []span{}
	first := 0
	overlap := 0
	hardStart := false
	for first < e.Tokens {
		last := e.Tokens
		hardEnd := false
		if e.Tokens-first > Parameters.BodyTokens {
			best := -1
			bestCategory := 5
			for cut := first + Parameters.MinimumBoundaryTokens; cut <= first+Parameters.BodyTokens; cut++ {
				end := boundary(text, e, cut)
				if end < e.Offsets[cut-1][1] || end <= boundary(text, e, first) {
					continue
				}
				rank := category(text, end)
				if rank <= bestCategory {
					bestCategory = rank
					best = cut
				}
			}
			if best < 0 {
				return nil, ErrUnsupported
			}
			last = best
			hardEnd = bestCategory == 4
		}
		start := boundary(text, e, first)
		if first == 0 {
			start = 0
		}
		end := boundary(text, e, last)
		if end <= start {
			return nil, ErrUnsupported
		}
		if len(spans) > 0 && end <= spans[len(spans)-1].end {
			return nil, ErrUnsupported
		}
		spans = append(spans, span{start: start, end: end, first: first, last: last, overlap: overlap, hardStart: hardStart, hardEnd: hardEnd})
		if len(spans) > Parameters.MaxSegments {
			return nil, ErrUnsupported
		}
		if last == e.Tokens {
			break
		}
		next := last - Parameters.OverlapTokens
		minimum := next - Parameters.OverlapBacktrack
		for next > minimum && !safeStart(text, boundary(text, e, next)) {
			next--
		}
		if next-first < Parameters.MinimumBoundaryTokens-Parameters.OverlapTokens-Parameters.OverlapBacktrack {
			return nil, ErrUnsupported
		}
		hardStart = !safeStart(text, boundary(text, e, next))
		overlap = last - next
		first = next
	}
	return spans, nil
}
