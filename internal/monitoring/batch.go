package monitoring

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"go.opentelemetry.io/otel/trace"

	"encoding/json"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// VersionReader reads the evaluated Record Version: its canonical text Parts
// and the metadata a rule may test.
type VersionReader interface {
	Article(ctx context.Context, org, corpusID, recordID, versionID string) (Article, error)
}

// Engine runs evaluation with bounded concurrency. Durable state lives in the
// store; a crash loses at most a lease, after which work is reclaimed and
// converges on the same Match identity.
type Engine struct {
	Store      EvaluationStore
	Versions   VersionReader
	Evaluators EvaluatorSet
	Workers    int
	Lease      time.Duration
	Poll       time.Duration
	// Group bounds how many intents of one Record Version and evaluator one
	// step evaluates together: default 64, and at most maxConcurrentCalls
	// times the evaluator's batch size.
	Group int
	// Metrics observes calls per Record Version and evaluations per call; nil ignores them.
	Metrics *EvaluationMetrics
	// Matched observes each committed Match by Organization and evaluator
	// plugin id (THE-798); nil observes nothing.
	Matched func(organization, evaluator string)
}

// EvaluatorKey identifies an installed evaluator implementation.
func EvaluatorKey(e Evaluator) string { return e.PluginID + "@" + e.Version }

const (
	maxExplanation = 4096
	maxPartKeys    = 100
	maxBackoff     = 5 * time.Minute
	defaultGroup   = 64
	// maxConcurrentCalls bounds the evaluator calls one step runs at once.
	maxConcurrentCalls = 4
	// splitCallsPerChunk bounds the calls spent isolating a failing
	// evaluation: enough to halve a 256-item chunk down to one item.
	splitCallsPerChunk = 18
)

// Run dispatches and evaluates until ctx ends.
func (e Engine) Run(ctx context.Context) {
	workers, poll := max(e.Workers, 1), e.Poll
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	var wg sync.WaitGroup
	wg.Add(workers + 1)
	go func() {
		defer wg.Done()
		e.loop(ctx, poll, func(ctx context.Context) (bool, error) {
			n, err := e.Store.FanOut(ctx)
			return n > 0, err
		}, "evaluation dispatch unavailable")
	}()
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			e.loop(ctx, poll, e.Step, "evaluation worker unavailable")
		}()
	}
	e.report(ctx)
	wg.Wait()
}

func (e Engine) loop(ctx context.Context, poll time.Duration, step func(context.Context) (bool, error), warning string) {
	for ctx.Err() == nil {
		// A step never outlives the lease of the work it claimed.
		work, admitted := lifecycle.Admit(ctx)
		if !admitted {
			return
		}
		attempt, cancel := context.WithTimeout(work, e.lease())
		progressed, err := step(attempt)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn(warning, "error", boundedError(err))
		}
		if progressed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

// report logs bounded backlog diagnostics periodically.
func (e Engine) report(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		read, cancel := context.WithTimeout(ctx, 5*time.Second)
		b, err := e.Store.Backlog(read)
		cancel()
		if err == nil {
			slog.Info("evaluation backlog", "pending_intents", b.Pending, "erroring_intents", b.Erroring)
		}
	}
}

// pending is one claimed evaluation intent, its pinned target and the index
// of the distinct batch item that decides it.
type pending struct {
	in     Intent
	target Target
	item   int
}

// Step claims and processes due work: one withdrawal intent, or the due
// evaluation intents of one Record Version pinned to one evaluator (up to the
// group bound), decided in batches of distinct evaluations. It returns false
// when no work was due.
func (e Engine) Step(ctx context.Context) (worked bool, stepErr error) {
	in, err := e.Store.Claim(ctx, e.lease())
	if errors.Is(err, ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if in.VersionID == "" {
		return e.processIntent(ctx, in)
	}
	err = workqueue.Track(ctx, in.Organization, "alert", fmt.Sprintf("%s:%d", in.SubscriptionVersionID, in.Sequence), in.VersionID, func(ctx context.Context) error { var err error; worked, err = e.processIntent(ctx, in); return err })
	return worked, err
}

func (e Engine) processIntent(ctx context.Context, in Intent) (worked bool, stepErr error) {
	ctx = telemetry.Restore(ctx, in.TraceContext)
	ctx, span := telemetry.Start(ctx, "monitoring.evaluate")
	defer func() { telemetry.Fail(span, stepErr); span.End() }()
	if in.Kind == IntentWithdrawal {
		outcome, err := e.Store.CommitWithdrawal(ctx, in)
		if err != nil {
			return true, e.retry(ctx, in, "storage_unavailable")
		}
		slog.InfoContext(ctx, "withdrawal notification committed", "organization", in.Organization, "subscription_id", in.SubscriptionID, "record_id", in.RecordID, "outcome", outcome)
		return true, nil
	}
	first, ok, err := e.admit(ctx, in)
	if !ok {
		return true, err
	}
	key := EvaluatorKey(first.target.Subscription.Evaluator)
	evaluator, installed := e.Evaluators.Evaluator(key)
	if !installed {
		return true, e.retry(ctx, in, "evaluator_unavailable")
	}
	group := []pending{first}
	if limit := e.groupLimit(evaluator); limit > 1 {
		related, err := e.Store.ClaimRelated(ctx, in, first.target.Subscription.Evaluator, limit-1, e.lease())
		if err != nil {
			// The first intent alone still makes progress.
			slog.WarnContext(ctx, "related evaluation claim unavailable", "error", boundedError(err))
		}
		for _, r := range related {
			p, ok, err := e.admit(ctx, r)
			if err != nil {
				slog.WarnContext(ctx, "evaluation admission unavailable", "error", boundedError(err))
			}
			if !ok {
				continue
			}
			if EvaluatorKey(p.target.Subscription.Evaluator) != key {
				_ = e.retry(ctx, r, "evaluator_unavailable")
				continue
			}
			group = append(group, p)
		}
	}
	article, err := e.Versions.Article(ctx, in.Organization, in.CorpusID, in.RecordID, in.VersionID)
	if err != nil {
		return true, e.retryAll(ctx, group, "content_unavailable")
	}
	if err := loadVectors(ctx, evaluator, e.Versions, in.Organization, in.CorpusID, in.VersionID, &article); err != nil {
		return true, e.retryAll(ctx, group, "content_unavailable")
	}
	items := distinctItems(group)
	batch := Batch{Organization: in.Organization, CorpusID: in.CorpusID, RecordID: in.RecordID, VersionID: in.VersionID, Enriched: first.target.Enriched, Article: article, Items: items}
	outcomes, calls := e.evaluate(ctx, evaluator, batch)
	e.Metrics.observeRecordVersion(calls)
	for _, p := range group {
		telemetry.Fail(span, outcomes[p.item].Err)
	}
	return true, e.applyGroup(ctx, group, article.Parts, outcomes)
}

// admit reads an intent's target and completes or retries an intent that
// must not be evaluated. ok reports an intent to evaluate.
func (e Engine) admit(ctx context.Context, in Intent) (pending, bool, error) {
	target, err := e.Store.Target(ctx, in)
	if errors.Is(err, ErrNotFound) {
		return pending{}, false, e.Store.Complete(ctx, in, OutcomeIneligible)
	}
	if err != nil {
		return pending{}, false, e.retry(ctx, in, "storage_unavailable")
	}
	if !target.Enabled {
		return pending{}, false, e.Store.Complete(ctx, in, OutcomeSubscriptionDisabled)
	}
	if target.Superseded {
		return pending{}, false, e.Store.Complete(ctx, in, OutcomeVersionSuperseded)
	}
	if target.Decided {
		// Another trigger of the same Version (enrichment after searchable)
		// found the pair already decided: do not ask the evaluator again.
		return pending{}, false, e.Store.Complete(ctx, in, OutcomeDuplicate)
	}
	return pending{in: in, target: target}, true, nil
}

func (e Engine) groupLimit(evaluator EvaluationPort) int {
	limit := e.Group
	if limit <= 0 {
		limit = defaultGroup
	}
	return max(1, min(limit, maxConcurrentCalls*max(evaluator.MaxBatch(), 1)))
}

// distinctItems deduplicates the group's (expression, configuration) pairs:
// each distinct pair is evaluated once and its decision fans back out to
// every intent that pins it, whatever its Subscription or owner. Items are
// ordered by their canonical JSON, so the same group yields the same batch.
func distinctItems(group []pending) []BatchItem {
	type keyed struct {
		key  string
		item BatchItem
	}
	byKey := map[string]int{}
	var distinct []keyed
	keys := make([]string, len(group))
	for i, p := range group {
		expression, configuration := p.target.Definition.Expression, p.target.Subscription.Evaluator.Configuration
		if expression == nil {
			expression = map[string]any{}
		}
		if configuration == nil {
			configuration = map[string]any{}
		}
		// encoding/json sorts object keys: equal values have equal keys.
		raw, _ := json.Marshal([]any{expression, configuration, p.target.QueryVectors})
		keys[i] = string(raw)
		ref := SubscriptionRef{SubscriptionID: p.in.SubscriptionID, SubscriptionVersionID: p.in.SubscriptionVersionID, SavedQueryID: p.target.Subscription.SavedQueryID, SavedQueryVersionID: p.target.Subscription.SavedQueryVersionID, Owner: p.target.Subscription.Owner}
		if j, ok := byKey[keys[i]]; ok {
			distinct[j].item.Subscriptions = append(distinct[j].item.Subscriptions, ref)
			continue
		}
		byKey[keys[i]] = len(distinct)
		distinct = append(distinct, keyed{key: keys[i], item: BatchItem{Expression: expression, Configuration: configuration, QueryVectors: p.target.QueryVectors, Subscriptions: []SubscriptionRef{ref}}})
	}
	sort.Slice(distinct, func(a, b int) bool { return distinct[a].key < distinct[b].key })
	items := make([]BatchItem, len(distinct))
	for j := range distinct {
		items[j] = distinct[j].item
		items[j].ID = fmt.Sprintf("e%d", j+1)
		byKey[distinct[j].key] = j
	}
	for i := range group {
		group[i].item = byKey[keys[i]]
	}
	return items
}

type VectorReader interface {
	ArticleVectors(context.Context, string, string, string) (*ArticleVectors, error)
}

func loadVectors(ctx context.Context, evaluator EvaluationPort, reader VersionReader, org, corpusID, versionID string, article *Article) error {
	requester, ok := evaluator.(interface{ WantsVectors() bool })
	if !ok || !requester.WantsVectors() {
		return nil
	}
	vectors, ok := reader.(VectorReader)
	if !ok {
		return nil
	}
	var err error
	article.Vectors, err = vectors.ArticleVectors(ctx, org, corpusID, versionID)
	return err
}

// evaluate decides every item in chunks of the evaluator's batch size, at
// most maxConcurrentCalls at once. A chunk whose request is too large, or
// whose call fails with a terminal plugin error or an invalid answer, is
// halved until the failing item fails alone, within a bounded number of
// calls per chunk. An unavailable plugin, or a retryable plugin error (a
// backend down), fails the whole chunk: it would fail every half alike. It
// returns one Outcome per item and the number of evaluator calls made.
func (e Engine) evaluate(ctx context.Context, evaluator EvaluationPort, b Batch) ([]Outcome, int) {
	outcomes := make([]Outcome, len(b.Items))
	size := max(evaluator.MaxBatch(), 1)
	var mu sync.Mutex
	calls := 0
	// Splits stop once the step has spent this many calls, so an error that
	// fails every evaluation costs a bounded number of calls per retry.
	budget := splitCallsPerChunk * ((len(b.Items) + size - 1) / size)
	var run func(lo, hi int)
	run = func(lo, hi int) {
		chunk := b
		chunk.Items = b.Items[lo:hi]
		got, err := evaluator.Evaluate(ctx, chunk)
		if err == nil && len(got) != len(chunk.Items) {
			err = fmt.Errorf("%w: %d outcomes for %d evaluations", ErrEvaluationInvalid, len(got), len(chunk.Items))
		}
		// A request refused before sending is not a call.
		if !errors.Is(err, ErrRequestTooLarge) {
			subscriptions := 0
			for _, item := range chunk.Items {
				subscriptions += len(item.Subscriptions)
			}
			mu.Lock()
			calls++
			e.Metrics.observeCall(len(chunk.Items), subscriptions)
			mu.Unlock()
		}
		mu.Lock()
		spent := calls
		mu.Unlock()
		// The protocol reports errors per call, not per evaluation: halve a
		// failed chunk so one evaluation the plugin cannot decide (an error
		// envelope or an invalid answer) keeps only itself pending. A plugin
		// that is unavailable fails every call alike, so it is not split.
		splittable := errors.Is(err, ErrRequestTooLarge) ||
			((errors.Is(err, ErrEvaluation) || errors.Is(err, ErrEvaluationInvalid)) && !errors.Is(err, ErrEvaluationRetryable) && spent < budget)
		if hi-lo > 1 && splittable {
			mid := lo + (hi-lo)/2
			run(lo, mid)
			run(mid, hi)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for i := lo; i < hi; i++ {
			if err != nil {
				outcomes[i] = Outcome{Err: err}
			} else {
				outcomes[i] = got[i-lo]
			}
		}
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxConcurrentCalls)
	for lo := 0; lo < len(b.Items); lo += size {
		hi := min(lo+size, len(b.Items))
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			run(lo, hi)
		}()
	}
	wg.Wait()
	return outcomes, calls
}

// applyGroup commits consecutive validated positives together when the store
// supports it. Flushing before another decision preserves the group's order,
// including corrections whose negative and positive decisions may interact.
func (e Engine) applyGroup(ctx context.Context, group []pending, parts []Part, outcomes []Outcome) error {
	store, batched := e.Store.(MatchBatchStore)
	var matches []MatchCommit
	var errs []error
	flush := func() {
		if len(matches) == 0 {
			return
		}
		commitCtx, commitSpan := commitContext(ctx, group[0].in.TraceContext, matches[0].Intent.TraceContext)
		if commitSpan != nil {
			defer commitSpan.End()
		}
		committed, err := store.CommitMatches(commitCtx, matches)
		if commitSpan != nil {
			telemetry.Fail(commitSpan, err)
		}
		if err != nil || len(committed) != len(matches) {
			for _, match := range matches {
				if err := e.retry(commitCtx, match.Intent, "storage_unavailable"); err != nil {
					errs = append(errs, err)
				}
			}
		} else {
			for i, match := range matches {
				e.observeMatch(commitCtx, match.Intent, match.Evidence, committed[i])
			}
		}
		matches = nil
	}
	for _, p := range group {
		result := outcomes[p.item]
		if batched && result.Err == nil && result.Decision == DecisionMatch {
			evidence := MatchEvidence{Evaluator: p.target.Subscription.Evaluator, Explanation: result.Explanation, PartKeys: result.PartKeys, Details: result.Details}
			if validEvidence(evidence, parts) {
				if len(matches) > 0 && matches[0].Intent.TraceContext != p.in.TraceContext {
					flush()
				}
				matches = append(matches, MatchCommit{Intent: p.in, Evidence: evidence})
				continue
			}
		}
		flush()
		commitCtx, commitSpan := commitContext(ctx, group[0].in.TraceContext, p.in.TraceContext)
		err := e.apply(commitCtx, p, parts, result)
		if commitSpan != nil {
			telemetry.Fail(commitSpan, err)
			commitSpan.End()
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	flush()
	return errors.Join(errs...)
}

// apply commits one intent's decision exactly as a single evaluation would:
// errors retry and never become negative, and a Match stays unique per
// Subscription Version and Record Version.
func (e Engine) apply(ctx context.Context, p pending, parts []Part, result Outcome) error {
	in := p.in
	if result.Err != nil {
		return e.retry(ctx, in, errorCode(result.Err))
	}
	switch result.Decision {
	case DecisionNoMatch:
		outcome, err := e.Store.CommitNoMatch(ctx, in)
		if err != nil {
			return e.retry(ctx, in, "storage_unavailable")
		}
		slog.InfoContext(ctx, "evaluation committed", "organization", in.Organization, "subscription_id", in.SubscriptionID, "record_version_id", in.VersionID, "outcome", outcome)
		return nil
	case DecisionNotReady:
		// A later trigger (for example enrichment) creates a new intent.
		return e.Store.Complete(ctx, in, OutcomeNotReady)
	case DecisionMatch:
	default:
		return e.retry(ctx, in, "evaluation_invalid")
	}
	evidence := MatchEvidence{Evaluator: p.target.Subscription.Evaluator, Explanation: result.Explanation, PartKeys: result.PartKeys, Details: result.Details}
	if !validEvidence(evidence, parts) {
		return e.retry(ctx, in, "evaluation_invalid")
	}
	outcome, err := e.Store.CommitMatch(ctx, in, evidence)
	if err != nil {
		return e.retry(ctx, in, "storage_unavailable")
	}
	e.observeMatch(ctx, in, evidence, outcome)
	return nil
}

func (e Engine) observeMatch(ctx context.Context, in Intent, evidence MatchEvidence, outcome string) {
	if outcome == OutcomeMatched && e.Matched != nil {
		e.Matched(in.Organization, evidence.Evaluator.PluginID)
	}
	slog.InfoContext(ctx, "evaluation committed", "organization", in.Organization, "subscription_id", in.SubscriptionID, "record_version_id", in.VersionID, "outcome", outcome)
}

// errorCode is the bounded retry code of an evaluation error.
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrEvaluatorUnavailable):
		return "evaluator_unavailable"
	case errors.Is(err, ErrEvaluatorConfiguration):
		return "evaluator_configuration_invalid"
	case errors.Is(err, ErrEvaluationInvalid):
		return "evaluation_invalid"
	case errors.Is(err, ErrRequestTooLarge):
		return "evaluation_request_too_large"
	default:
		return "evaluator_error"
	}
}

func (e Engine) retryAll(ctx context.Context, group []pending, code string) error {
	var errs []error
	for _, p := range group {
		if err := e.retry(ctx, p.in, code); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Commits with another durable parent link back to their shared evaluation.
func commitContext(ctx context.Context, parent, stored string) (context.Context, trace.Span) {
	if stored == parent {
		return ctx, nil
	}
	return telemetry.Start(telemetry.Restore(ctx, stored), "monitoring.commit", trace.WithLinks(trace.Link{SpanContext: trace.SpanContextFromContext(ctx)}))
}
