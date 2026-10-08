package connectors

import (
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"
)

type timingStage int

const (
	stageGrant timingStage = iota
	stageUpload
	stageVerify
	stageAccept
	stageReceipt
	stageDescribe
	stageCount
)

// Worker durations are sums, not elapsed page time: concurrent calls overlap.
type pageTimings struct {
	stages  [stageCount]atomic.Int64
	started time.Time
	fetch   time.Duration
	items   int
}

func (a Acquirer) measure(t *pageTimings, stage timingStage) func() {
	if t == nil { // Push submissions have no acquisition page.
		return func() {}
	}
	started := a.now()
	return func() { t.stages[stage].Add(int64(max(time.Duration(0), a.now().Sub(started)))) }
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func (t *pageTimings) diagnostic(now, runStarted time.Time, more bool, reason string, interval time.Duration) map[string]any {
	return map[string]any{
		"items": t.items, "fetch_ms": milliseconds(t.fetch),
		"grant_ms":    milliseconds(time.Duration(t.stages[stageGrant].Load())),
		"upload_ms":   milliseconds(time.Duration(t.stages[stageUpload].Load())),
		"verify_ms":   milliseconds(time.Duration(t.stages[stageVerify].Load())),
		"accept_ms":   milliseconds(time.Duration(t.stages[stageAccept].Load())),
		"receipt_ms":  milliseconds(time.Duration(t.stages[stageReceipt].Load())),
		"describe_ms": milliseconds(time.Duration(t.stages[stageDescribe].Load())),
		"page_ms":     milliseconds(max(time.Duration(0), now.Sub(t.started))),
		"run_ms":      milliseconds(max(time.Duration(0), now.Sub(runStarted))),
		"more":        more, "stop_reason": reason, "interval_ms": milliseconds(interval),
		"continuation": false, "continuation_reason": reason,
	}
}

// Preserve kind diagnostics and the protocol's 16 KiB bound. A full diagnostic
// object (or a kind that already owns this key) still gets timings in logs.
func acquisitionDiagnostics(raw json.RawMessage, timing map[string]any) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return raw // Omission retains the previously committed kind diagnostics.
	}
	fields := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return raw
	}
	if _, exists := fields["acquisition"]; exists {
		return raw
	}
	fields["acquisition"], _ = json.Marshal(timing)
	merged, err := json.Marshal(fields)
	if err != nil || len(merged) > 16<<10 {
		return raw
	}
	return merged
}

func logPageTiming(id string, run int64, page int, outcome string, timing map[string]any) {
	slog.Info("connector acquisition page", "connector_id", id, "run", run, "page", page, "outcome", outcome, "acquisition", timing)
}
