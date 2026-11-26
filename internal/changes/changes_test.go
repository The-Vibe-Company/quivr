package changes

import (
	"context"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

type resumeJournal struct {
	start Resume
	seen  []Resume
}

func (j *resumeJournal) ReadChanges(_ context.Context, _ string, _ string, after int64, limit int, _ time.Duration) (Window, error) {
	return j.read(after, "", limit), nil
}

func (j *resumeJournal) StartChanges(context.Context, string, string, time.Duration) (Resume, error) {
	return j.start, nil
}

func (j *resumeJournal) ReadChangesResume(_ context.Context, _ string, _ string, resume Resume, limit int, _ time.Duration) (Window, error) {
	j.seen = append(j.seen, resume)
	return j.read(resume.Position, resume.StartID, limit), nil
}

func (j *resumeJournal) read(after int64, startID string, limit int) Window {
	w := Window{Head: 12, Through: after, ResumeSet: true, StartCeiling: 10}
	w.Resume = Resume{Position: after, StartID: startID}
	if limit > 0 && after < 12 {
		w.Events = []Event{{Position: after + 1, ID: "event", Type: "record.accepted", CorpusID: "corpus", ResourceKind: "record", ResourceID: "record", OccurredAt: time.Unix(0, 0).UTC()}}
		w.Through = w.Events[0].Position
		if w.Through >= w.StartCeiling {
			w.Resume.StartID = ""
		}
	}
	w.Resume.Position = w.Through
	return w
}

func testScope() corpus.Scope {
	return corpus.Scope{Organization: "org", Actions: []string{"changes:read"}, Corpora: []string{"corpus"}}
}

// TestResumeStateSurvivesServiceReadsAndV1RemainsNumeric owns the service
// contract that snapshot state travels through event and next cursors. The
// PostgreSQL owner covers snapshot visibility and pruning; this cheaper test
// catches a transport-facing cursor that silently drops StartID.
func TestResumeStateSurvivesServiceReadsAndV1RemainsNumeric(t *testing.T) {
	journal := &resumeJournal{start: Resume{Position: 4, StartID: "start-1"}}
	service := Service{Journal: journal, Key: []byte("cursor-key")}
	scope := testScope()

	start, err := service.Start(context.Background(), scope, "corpus", "")
	if err != nil || start != journal.start {
		t.Fatalf("start=%+v err=%v, want %+v", start, err, journal.start)
	}
	page, err := service.Read(context.Background(), scope, "corpus", start, 1)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("read page=%+v err=%v", page, err)
	}
	if page.Items[0].Cursor == service.Cursor(scope, "corpus", page.Items[0].Position) {
		t.Fatal("event cursor dropped its snapshot reference")
	}
	resumed, err := service.Start(context.Background(), scope, "corpus", page.Items[0].Cursor)
	if err != nil || resumed.StartID != start.StartID || resumed.Position != page.Items[0].Position {
		t.Fatalf("event cursor resume=%+v err=%v, want position %d and start %q", resumed, err, page.Items[0].Position, start.StartID)
	}

	v1 := service.Cursor(scope, "corpus", 7)
	// Adding audit attribution to an existing key leaves its cursor valid.
	scope.ActorID = "38e61a6d8a6f4849aca7b0f7dd7880d6"
	legacy, err := service.Start(context.Background(), scope, "corpus", v1)
	if err != nil || legacy.Position != 7 || legacy.StartID != "" {
		t.Fatalf("v1 resume=%+v err=%v", legacy, err)
	}
}
