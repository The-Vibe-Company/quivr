package content_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// TestTimelineTimesEachStepFromItsCause owns the timeline contract: finished
// steps in time order, each timed from the step that causes it, and the
// plugin that ran it.
func TestTimelineTimesEachStepFromItsCause(t *testing.T) {
	base := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	at := func(ms int) *time.Time { v := base.Add(time.Duration(ms) * time.Millisecond); return &v }
	ingest := &content.PluginRef{ID: "core.ingest", Version: "1.0.0"}
	pdf := &content.PluginRef{ID: "pdf-text", Version: "0.1.0"}
	cases := []struct {
		name string
		in   content.Activity
		want string
	}{
		{"received only", content.Activity{Steps: content.Steps{Accepted: at(0)}}, "accepted@0"},
		{"history: only acceptance known", content.Activity{Steps: content.Steps{Accepted: at(0), Withdrawn: at(900)}}, "accepted@0 withdrawn@900"},
		{
			// Alerts may settle before vectors: both are timed from searchable,
			// and the order follows the clock, not the pipeline.
			"full pipeline",
			content.Activity{Normalizer: pdf, Ingestion: ingest, Steps: content.Steps{Accepted: at(0), Materialized: at(400), Segmented: at(1100), RetrievalReady: at(1600), Enriched: at(2900), Evaluated: at(2000)}},
			"accepted@0 materialized@400<accepted+400ms[pdf-text@0.1.0] segmented@1100<materialized+700ms[core.ingest@1.0.0] retrieval_ready@1600<segmented+500ms[core.ingest@1.0.0] evaluated@2000<retrieval_ready+400ms enriched@2900<retrieval_ready+1.3s[core.ingest@1.0.0]",
		},
		{"quarantined at publication", content.Activity{Steps: content.Steps{Accepted: at(0), Materialized: at(300), Quarantined: at(300)}}, "accepted@0 materialized@300<accepted+300ms quarantined@300<materialized+0s"},
		{"quarantined while building the baseline", content.Activity{Steps: content.Steps{Accepted: at(0), Materialized: at(300), Segmented: at(500), Quarantined: at(800)}}, "accepted@0 materialized@300<accepted+300ms segmented@500<materialized+200ms quarantined@800<segmented+300ms"},
		{"a cause dated after its step gives no duration", content.Activity{Steps: content.Steps{Accepted: at(0), Enriched: at(200), RetrievalReady: at(900)}}, "accepted@0 enriched@200 retrieval_ready@900"},
		{"a cause without a time gives no duration", content.Activity{Steps: content.Steps{Materialized: at(300), Segmented: at(500)}}, "materialized@300 segmented@500<materialized+200ms"},
	}
	for _, c := range cases {
		var got []string
		for _, s := range content.Timeline(c.in) {
			line := fmt.Sprintf("%s@%d", s.Step, s.At.Sub(base).Milliseconds())
			if s.Since != "" {
				line += fmt.Sprintf("<%s+%s", s.Since, s.Duration)
			}
			if s.Plugin != nil {
				line += fmt.Sprintf("[%s@%s]", s.Plugin.ID, s.Plugin.Version)
			}
			got = append(got, line)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, strings.Join(got, " "), c.want)
		}
	}
}
