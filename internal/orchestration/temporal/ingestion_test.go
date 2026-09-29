package temporal

import (
	"context"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// ingestionRun executes the ingestion workflow with fake activities: process
// answers the successive process-token-windows-v2 results.
func ingestionRun(t *testing.T, process ...ProcessResult) (normalized, processed, enriched int, err error) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(materializeWorkflow, workflow.RegisterOptions{Name: "process-e5-v3"})
	env.RegisterActivityWithOptions(func(context.Context, Input) (ProcessResult, error) {
		r := process[min(processed, len(process)-1)]
		processed++
		return r, nil
	}, activity.RegisterOptions{Name: "process-token-windows-v2"})
	env.RegisterActivityWithOptions(func(context.Context, Input) error { normalized++; return nil }, activity.RegisterOptions{Name: "normalize-external"})
	env.RegisterActivityWithOptions(func(context.Context, Input) error { enriched++; return nil }, activity.RegisterOptions{Name: "enrich-e5"})
	env.ExecuteWorkflow("process-e5-v3", Input{Organization: "org_a", ReceiptID: "receipt_1"})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	return normalized, processed, enriched, env.GetWorkflowError()
}

func TestContentWithoutNormalizationRunsNoNormalizationActivity(t *testing.T) {
	normalized, processed, enriched, err := ingestionRun(t, ProcessResult{})
	if err != nil || normalized != 0 || processed != 1 || enriched != 1 {
		t.Fatalf("normalized %d processed %d enriched %d err %v", normalized, processed, enriched, err)
	}
}

func TestRoutedBlobsNormalizeOnceBeforePublication(t *testing.T) {
	normalized, processed, enriched, err := ingestionRun(t, ProcessResult{NormalizationRequired: true}, ProcessResult{})
	if err != nil || normalized != 1 || processed != 2 || enriched != 1 {
		t.Fatalf("normalized %d processed %d enriched %d err %v", normalized, processed, enriched, err)
	}
}

func TestNormalizationWithoutAnOutcomeStopsAfterBoundedRounds(t *testing.T) {
	normalized, _, enriched, err := ingestionRun(t, ProcessResult{NormalizationRequired: true})
	if err == nil || normalized != maxNormalizationRounds || enriched != 0 {
		t.Fatalf("normalized %d enriched %d err %v", normalized, enriched, err)
	}
}
