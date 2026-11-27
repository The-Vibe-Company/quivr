package plugin

import (
	"encoding/json"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

type configuration struct {
	Fusion              string  `json:"fusion"`
	DenseWeight         float64 `json:"dense_weight"`
	CandidateCount      int     `json:"candidate_count"`
	DenseK              int     `json:"dense_k"`
	DBSFClamp           bool    `json:"dbsf_clamp"`
	RRFK                int     `json:"rrf_k"`
	TitleLeadWeight     float64 `json:"title_lead_weight"`
	LeadChars           int     `json:"lead_chars"`
	ConcentrationWeight float64 `json:"concentration_weight"`
}

func defaultConfig(limit int) configuration {
	lexK := 60
	if limit > lexK {
		lexK = min(limit, 100)
	}
	return configuration{
		Fusion:              "convex",
		DenseWeight:         0.65,
		CandidateCount:      lexK,
		DenseK:              100,
		DBSFClamp:           false,
		RRFK:                60,
		TitleLeadWeight:     0.12,
		LeadChars:           250,
		ConcentrationWeight: 0.12,
	}
}

func parseConfig(raw json.RawMessage, limit int) (configuration, error) {
	cfg := defaultConfig(limit)
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return configuration{}, quivrplugin.TerminalSearchError("invalid_configuration", err.Error())
	}
	if cfg.CandidateCount < 1 {
		cfg.CandidateCount = max(limit, 1)
	}
	if cfg.CandidateCount > 100 {
		cfg.CandidateCount = 100
	}
	if cfg.DenseK < 1 {
		cfg.DenseK = 100
	}
	if cfg.DenseK > 100 {
		cfg.DenseK = 100
	}
	if cfg.RRFK < 1 {
		cfg.RRFK = 60
	}
	if cfg.LeadChars < 50 {
		cfg.LeadChars = 250
	}
	return cfg, nil
}
