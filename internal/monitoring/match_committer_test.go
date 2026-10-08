package monitoring_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
)

// The store records ready commits; SQL identity and atomicity belong to the
// real PostgreSQL owner. synctest makes the partial flush deterministic without
// a production timing hook or a wall-clock wait.
type readyMatchStore struct {
	*fakeEvaluation
	mu        sync.Mutex
	calls     chan []monitoring.MatchCommit
	retries   chan string
	mode      string
	claimNext chan struct{}
	release   chan struct{}
}

func (s *readyMatchStore) Claim(ctx context.Context, _ time.Duration) (monitoring.Intent, error) {
	s.mu.Lock()
	if len(s.intents) == 0 {
		s.mu.Unlock()
		return monitoring.Intent{}, monitoring.ErrNoWork
	}
	in := s.intents[0]
	s.intents = s.intents[1:]
	s.mu.Unlock()
	if s.mode == "grace" {
		s.claimNext <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return monitoring.Intent{}, ctx.Err()
		}
	}
	if s.mode == "organizations" && in.VersionID == "second" {
		select {
		case <-s.claimNext:
		case <-ctx.Done():
			return monitoring.Intent{}, ctx.Err()
		}
	}
	return in, nil
}
func (s *readyMatchStore) ClaimRelated(context.Context, monitoring.Intent, monitoring.Evaluator, int, time.Duration) ([]monitoring.Intent, error) {
	return nil, nil
}
func (s *readyMatchStore) Target(context.Context, monitoring.Intent) (monitoring.Target, error) {
	return s.target, nil
}
func (s *readyMatchStore) Retry(ctx context.Context, in monitoring.Intent, _ string, _ time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.retries <- in.VersionID
	return nil
}
func (s *readyMatchStore) CommitMatches(ctx context.Context, group []monitoring.MatchCommit) ([]string, error) {
	s.calls <- append([]monitoring.MatchCommit(nil), group...)
	if s.mode == "organizations" && group[0].Intent.Organization == "first" {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.mode == "shutdown" || s.mode == "grace-deadline" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.mode == "poison" && (len(group) > 1 || group[0].Intent.VersionID == "second") {
		return nil, errors.New("one ready group is invalid")
	}
	out := make([]string, len(group))
	for i, m := range group {
		out[i] = monitoring.OutcomeMatched
		if m.Intent.VersionID == "second" && s.mode != "organizations" {
			out[i] = monitoring.OutcomeDuplicate
		}
	}
	return out, nil
}

// Owns isolation between Organization journals: a store call deliberately
// blocked for one Organization cannot delay an independently ready sibling.
func TestReadyMatchCommitsKeepOrganizationsIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		base, engine, _ := batchScenario(&phraseEvaluator{max: 32}, "strike")
		store := &readyMatchStore{fakeEvaluation: base, mode: "organizations", calls: make(chan []monitoring.MatchCommit, 8), claimNext: make(chan struct{}), release: make(chan struct{})}
		store.target = base.targets["subv-0"]
		store.intents = nil
		for _, v := range []string{"first", "second"} {
			store.intents = append(store.intents, monitoring.Intent{Organization: v, CorpusID: "c", SubscriptionID: "s", SubscriptionVersionID: "sv", RecordID: v, VersionID: v})
		}
		engine.Store, engine.Workers = store, 2
		matched := make(chan string, 2)
		engine.Matched = func(org, _ string) { matched <- org }
		done := make(chan struct{})
		go func() { engine.Run(ctx); close(done) }()
		defer func() { cancel(); close(store.release); <-done }()
		if first := <-store.calls; len(first) != 1 || first[0].Intent.Organization != "first" {
			t.Fatalf("blocked Organization commit: %+v", first)
		}
		close(store.claimNext)
		select {
		case next := <-store.calls:
			if len(next) != 1 || next[0].Intent.Organization != "second" {
				t.Fatalf("Organization journals were mixed: %+v", next)
			}
		case <-ctx.Done():
			t.Fatal("one Organization blocked an independently ready commit")
		}
		synctest.Wait()
		if len(matched) != 1 || <-matched != "second" {
			t.Fatal("independent positive did not finish while the first was blocked")
		}
	})
}

// Owns the public Engine.Run/store boundary: ready Versions share a commit,
// outcomes reach the right evaluation, shutdown waits for transaction outcome,
// and one poisoned request leaves healthy siblings able to commit.
func TestEngineRunSharesReadyMatchCommits(t *testing.T) {
	for _, mode := range []string{"outcomes", "shutdown", "poison", "grace", "grace-deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				var cancel context.CancelFunc
				var process *lifecycle.Group
				if mode == "grace" || mode == "grace-deadline" {
					process = lifecycle.New()
					ctx, cancel = process.Context(), process.BeginDrain
				} else {
					ctx, cancel = context.WithCancel(ctx)
				}
				store := &readyMatchStore{fakeEvaluation: &fakeEvaluation{}, calls: make(chan []monitoring.MatchCommit, 8), retries: make(chan string, 8), mode: mode}
				if mode == "grace" {
					store.claimNext, store.release = make(chan struct{}, 2), make(chan struct{})
				}
				for _, v := range []string{"first", "second"} {
					store.intents = append(store.intents, monitoring.Intent{Organization: "org", CorpusID: "c", SubscriptionID: "s", SubscriptionVersionID: "sv", RecordID: v, VersionID: v})
				}
				store.target = monitoring.Target{Enabled: true, Definition: monitoring.Definition{Expression: map[string]any{"text": "strike"}}, Subscription: monitoring.SubscriptionVersion{Evaluator: monitoring.Evaluator{PluginID: "acme.alerts", Version: "0.1.0", Configuration: map[string]any{}}}}
				matched := make(chan string, 2)
				engine := monitoring.Engine{Store: store, Versions: parts{{Key: "body", Role: "body", Text: "Harbour strike"}}, Evaluators: monitoring.Evaluators{alertsKey: &phraseEvaluator{max: 32}}, Workers: 2,
					Matched: func(org, evaluator string) { matched <- org + "/" + evaluator }}
				done := make(chan struct{})
				run := func(ctx context.Context) { engine.Run(ctx); close(done) }
				if process != nil {
					process.Go(run)
				} else {
					go run(ctx)
				}
				defer func() {
					cancel()
					if process != nil {
						process.Close()
					}
					<-done
				}()
				if mode == "grace" {
					<-store.claimNext
					<-store.claimNext
					process.BeginDrain()
					close(store.release)
				}
				var first []monitoring.MatchCommit
				select {
				case first = <-store.calls:
				case <-done:
					t.Fatal("admitted positives did not commit during grace")
				}
				if len(first) != 2 || first[0].Intent.VersionID == first[1].Intent.VersionID {
					t.Fatalf("ready Versions did not share a commit: %+v", first)
				}
				if mode == "shutdown" {
					cancel()
					<-done
					return
				}
				if mode == "grace-deadline" {
					process.BeginDrain()
					synctest.Wait()
					select {
					case <-done:
						t.Fatal("admission shutdown canceled an active commit before grace expired")
					default:
					}
					deadline, end := context.WithTimeout(context.Background(), time.Second)
					defer end()
					if err := process.Wait(deadline); !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("shared grace deadline: %v", err)
					}
					<-done
					return
				}
				if mode == "poison" {
					if retried := <-store.retries; retried != "second" {
						t.Fatalf("poison retry belonged to %s", retried)
					}
				}
				synctest.Wait()
				if len(matched) != 1 || <-matched != "org/acme.alerts" {
					t.Fatal("per-intent outcomes lost the healthy positive or counted a duplicate")
				}
			})
		})
	}
}
