package plugin

import (
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

type retriever struct{}

// New creates a retriever for the plugin SDK.
func New() quivrplugin.Retriever { return retriever{} }

func (retriever) Search(_ context.Context, req *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	cfg, err := parseConfig(req.Configuration, req.Limit)
	if err != nil {
		return nil, err
	}
	if req.Round == 1 {
		reqs, err := buildRound1Requests(req, cfg)
		if err != nil {
			return nil, err
		}
		return quivrplugin.Ask(reqs...), nil
	}
	return scoreAndRank(req, cfg), nil
}

func buildRound1Requests(req *quivrplugin.SearchRequest, cfg configuration) ([]quivrplugin.CandidateRequest, error) {
	bm25Req := quivrplugin.CandidateRequest{
		Primitive: quivrplugin.PrimitiveBM25,
		QueryText: req.Query.Text,
		Field:     quivrplugin.FieldSource,
		K:         cfg.CandidateCount,
		GroupBy:   "record",
	}
	if req.Query.Mode == "lexical" {
		return []quivrplugin.CandidateRequest{bm25Req}, nil
	}

	var servedSpaces []quivrplugin.SearchSpace
	for _, sp := range req.Spaces {
		if sp.Role == "served" {
			servedSpaces = append(servedSpaces, sp)
		}
	}
	if len(servedSpaces) == 0 {
		if req.Query.Mode == "hybrid" {
			return []quivrplugin.CandidateRequest{bm25Req}, nil
		}
		return nil, quivrplugin.TerminalSearchError("no_served_space", "the searched Corpora serve no vector space; search by keywords, or rebuild them")
	}

	var requests []quivrplugin.CandidateRequest
	if req.Query.Mode == "hybrid" {
		requests = append(requests, bm25Req)
	}
	for _, sp := range servedSpaces {
		if len(requests) >= 8 {
			break
		}
		// One ungrouped near_vector leg supplies both MaxP and multi-passage
		// concentration in a single index request. Image-only spaces stay grouped.
		groupBy := ""
		k := cfg.DenseK
		if isImageOnlySpace(sp.Indexes) {
			groupBy = "record"
			k = cfg.CandidateCount
		}
		requests = append(requests, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveNearVector,
			QueryText: req.Query.Text,
			Space:     sp.ID,
			K:         k,
			GroupBy:   groupBy,
		})
	}
	return requests, nil
}

type recordState struct {
	recordID string

	hasLex   bool
	lexScore float64
	lexSeg   quivrplugin.Candidate

	hasDense  bool
	denseMaxP float64
	denseBest quivrplugin.Candidate
	denseSegs map[string]struct{}

	titleText string
	leadText  string
}

func (r *recordState) observeTitleLead(c quivrplugin.Candidate, cfg configuration) {
	if cfg.TitleLeadWeight <= 0 {
		return
	}
	t, l := extractTitleAndLead(c, cfg.LeadChars)
	if t != "" && len(t) > len(r.titleText) {
		r.titleText = t
	}
	if l != "" && len(l) > len(r.leadText) {
		r.leadText = l
	}
}

func scoreAndRank(req *quivrplugin.SearchRequest, cfg configuration) *quivrplugin.SearchAnswer {
	records := map[string]*recordState{}
	getRecord := func(c quivrplugin.Candidate) *recordState {
		key := c.RecordID
		if key == "" {
			key = c.SegmentID
		}
		st, ok := records[key]
		if !ok {
			st = &recordState{recordID: key}
			records[key] = st
		}
		return st
	}

	for _, served := range req.Served {
		switch served.Request.Primitive {
		case quivrplugin.PrimitiveBM25:
			for _, c := range served.Candidates {
				st := getRecord(c)
				if !st.hasLex || c.Score > st.lexScore || (c.Score == st.lexScore && c.SegmentID < st.lexSeg.SegmentID) {
					st.hasLex = true
					st.lexScore = c.Score
					st.lexSeg = c
				}
				st.observeTitleLead(c, cfg)
			}
		case quivrplugin.PrimitiveNearVector:
			for _, c := range served.Candidates {
				st := getRecord(c)
				if st.denseSegs == nil {
					st.denseSegs = map[string]struct{}{}
				}
				st.denseSegs[c.SegmentID] = struct{}{}
				if !st.hasDense || c.Score > st.denseMaxP || (c.Score == st.denseMaxP && c.SegmentID < st.denseBest.SegmentID) {
					st.hasDense = true
					st.denseMaxP = c.Score
					st.denseBest = c
				}
				st.observeTitleLead(c, cfg)
			}
		}
	}

	if len(records) == 0 {
		return quivrplugin.Rank()
	}

	keys := make([]string, 0, len(records))
	for k := range records {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	lexRaw := make(map[string]float64, len(keys))
	denseRaw := make(map[string]float64, len(keys))
	for _, k := range keys {
		st := records[k]
		if st.hasLex {
			lexRaw[k] = st.lexScore
		}
		if st.hasDense {
			denseRaw[k] = st.denseMaxP
		}
	}

	lexNorm := normalizeLeg(lexRaw, cfg)
	denseNorm := normalizeLeg(denseRaw, cfg)

	var queryTerms []string
	var idf map[string]float64
	if cfg.TitleLeadWeight > 0 {
		queryTerms = uniqueTerms(req.Query.Text)
		if len(queryTerms) > 0 {
			docTermSets := make([]map[string]bool, 0, len(keys))
			for _, k := range keys {
				st := records[k]
				combined := st.titleText
				if st.leadText != "" {
					if combined != "" {
						combined += " " + st.leadText
					} else {
						combined = st.leadText
					}
				}
				docTermSets = append(docTermSets, termSet(combined))
			}
			idf = computeInPoolIDF(queryTerms, docTermSets)
		}
	}

	alpha := cfg.DenseWeight
	if len(lexRaw) == 0 {
		alpha = 1.0
	} else if len(denseRaw) == 0 {
		alpha = 0.0
	}

	hits := make([]quivrplugin.RankedHit, 0, len(keys))
	for _, k := range keys {
		st := records[k]
		score := (1.0-alpha)*lexNorm[k] + alpha*denseNorm[k]

		if cfg.TitleLeadWeight > 0 && len(queryTerms) > 0 {
			score += cfg.TitleLeadWeight * titleLeadCoverage(queryTerms, idf, st.titleText, st.leadText)
		}
		if cfg.ConcentrationWeight > 0 && len(st.denseSegs) > 1 {
			conc := math.Min(1.0, math.Log(float64(len(st.denseSegs)))/math.Log(4.0))
			score += cfg.ConcentrationWeight * conc
		}

		repSeg := st.lexSeg
		if st.hasDense {
			repSeg = st.denseBest
		}
		hits = append(hits, quivrplugin.RankedHit{
			SegmentID:   repSeg.SegmentID,
			Score:       score,
			Explanation: formatExplanation(cfg, st),
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].SegmentID < hits[j].SegmentID
	})
	if len(hits) > req.Limit {
		hits = hits[:req.Limit]
	}
	return quivrplugin.Rank(hits...)
}

func normalizeLeg(raw map[string]float64, cfg configuration) map[string]float64 {
	out := make(map[string]float64, len(raw))
	if len(raw) == 0 {
		return out
	}

	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	switch cfg.Fusion {
	case "rrf":
		sort.Slice(keys, func(i, j int) bool {
			if raw[keys[i]] != raw[keys[j]] {
				return raw[keys[i]] > raw[keys[j]]
			}
			return keys[i] < keys[j]
		})
		scale := float64(cfg.RRFK + 1)
		for idx, k := range keys {
			out[k] = scale / float64(cfg.RRFK+idx+1)
		}
		return out

	case "dbsf":
		n := float64(len(keys))
		sum := 0.0
		for _, k := range keys {
			sum += raw[k]
		}
		mean := sum / n
		varSum := 0.0
		for _, k := range keys {
			d := raw[k] - mean
			varSum += d * d
		}
		std := math.Sqrt(varSum / n)
		if std < 1e-9 {
			for _, k := range keys {
				out[k] = 0.5
			}
			return out
		}
		for _, k := range keys {
			z := 0.5 + (raw[k]-mean)/(6.0*std)
			if cfg.DBSFClamp {
				z = math.Max(0.0, math.Min(1.0, z))
			}
			out[k] = z
		}
		return out

	default: // "convex" (Bruch et al. min-max convex combination)
		minV, maxV := math.Inf(1), math.Inf(-1)
		for _, k := range keys {
			v := raw[k]
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
		span := maxV - minV
		if span < 1e-12 {
			for _, k := range keys {
				out[k] = 1.0
			}
			return out
		}
		for _, k := range keys {
			out[k] = (raw[k] - minV) / span
		}
		return out
	}
}

func titleLeadCoverage(queryTerms []string, idf map[string]float64, title, lead string) float64 {
	if len(queryTerms) == 0 || (title == "" && lead == "") {
		return 0
	}
	titleSet := termSet(title)
	leadSet := termSet(lead)
	totalIDF := 0.0
	matchedIDF := 0.0
	for _, qt := range queryTerms {
		w := idf[qt]
		totalIDF += w
		if titleSet[qt] || leadSet[qt] {
			matchedIDF += w
		}
	}
	if totalIDF <= 0 {
		return 0
	}
	return math.Min(1.0, matchedIDF/totalIDF)
}

func formatExplanation(cfg configuration, st *recordState) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("%s alpha=%g", cfg.Fusion, cfg.DenseWeight))
	switch {
	case st.hasLex && st.hasDense:
		parts = append(parts, "lex+dense")
	case st.hasDense:
		parts = append(parts, "dense")
	default:
		parts = append(parts, "lex")
	}
	if cfg.TitleLeadWeight > 0 {
		parts = append(parts, fmt.Sprintf("tl=%g", cfg.TitleLeadWeight))
	}
	if cfg.ConcentrationWeight > 0 && len(st.denseSegs) > 1 {
		parts = append(parts, fmt.Sprintf("chunks=%d", len(st.denseSegs)))
	}
	return strings.Join(parts, " ")
}

func isImageOnlySpace(indexes []string) bool {
	return slices.Contains(indexes, "image") && !slices.Contains(indexes, "text") && !slices.Contains(indexes, "hybrid")
}

// Main runs the newsroom.retrieve sidecar process.
func Main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Retrieval(New())
	}
	if err == nil {
		m := plugin.Manifest()
		err = plugin.ServeReady(func(addr string) {
			fmt.Fprintf(os.Stderr, "quivr-newsroom-retrieve: serving %s@%s (retrieval) on %s\n", m.ID, m.Version, addr)
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quivr-newsroom-retrieve:", err)
		os.Exit(1)
	}
}
