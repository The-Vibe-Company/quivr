package corpus

import (
	"errors"
	"testing"
)

// This table owns the action-to-grant security contract. Each grant is removed
// in turn so weakening a conjunctive requirement cannot silently allow access.
func TestActionPermissions(t *testing.T) {
	cases := []struct {
		action     Action
		grants     []string
		allCorpora bool
	}{
		{ActionAuditRead, []string{"audit:read"}, true},
		{ActionBackfillPromote, []string{"plugins:admin"}, false},
		{ActionBackfillRequest, []string{"plugins:admin"}, false},
		{ActionConnectorsCreate, []string{"connectors:write"}, false},
		{ActionConnectorsRead, []string{"connectors:read"}, false},
		{ActionConnectorsList, []string{"connectors:read"}, false},
		{ActionConnectorsDisable, []string{"connectors:write"}, false},
		{ActionConnectorsReplaceCredential, []string{"connectors:write"}, false},
		{ActionConnectorsKinds, []string{"connectors:read"}, false},
		{ActionConnectorsChangeSchedule, []string{"connectors:write"}, false},
		{ActionConnectorsRequestRun, []string{"connectors:write"}, false},
		{ActionActivityLatest, []string{"observability:read"}, true},
		{ActionActivityVersion, []string{"observability:read"}, false},
		{ActionContentBatch, []string{"content:write"}, false},
		{ActionContentAccept, []string{"content:write"}, false},
		{ActionContentWithdraw, []string{"content:write"}, false},
		{ActionContentReceipt, []string{"content:read"}, false},
		{ActionContentRecord, []string{"content:read"}, false},
		{ActionContentRecords, []string{"content:read"}, false},
		{ActionContentVersion, []string{"content:read"}, false},
		{ActionContentHydrate, []string{"content:read", "search:query"}, false},
		{ActionCorpusCreate, []string{"corpora:write"}, true},
		{ActionCorpusRead, []string{"corpora:read"}, false},
		{ActionCorpusList, []string{"corpora:read"}, false},
		{ActionMonitoringSavedQueryVersion, []string{"monitoring:read"}, false},
		{ActionMonitoringSubscriptionVersion, []string{"monitoring:read"}, false},
		{ActionMonitoringAttempts, []string{"monitoring:read"}, false},
		{ActionMonitoringMatches, []string{"monitoring:read"}, false},
		{ActionMonitoringMatch, []string{"monitoring:read"}, false},
		{ActionMonitoringDelivery, []string{"monitoring:read"}, false},
		{ActionMonitoringMigrateEvaluator, []string{"plugins:admin"}, false},
		{ActionMonitoringCreateSavedQuery, []string{"monitoring:write"}, false},
		{ActionMonitoringSavedQuery, []string{"monitoring:read"}, false},
		{ActionMonitoringCreateSavedQueryVersion, []string{"monitoring:write"}, false},
		{ActionMonitoringDeleteSavedQuery, []string{"monitoring:write"}, false},
		{ActionMonitoringRenameSavedQuery, []string{"monitoring:write"}, false},
		{ActionMonitoringCreateSubscription, []string{"monitoring:write"}, false},
		{ActionMonitoringSubscriptions, []string{"monitoring:read"}, false},
		{ActionMonitoringSubscription, []string{"monitoring:read"}, false},
		{ActionMonitoringCreateSubscriptionVersion, []string{"monitoring:write"}, false},
		{ActionMonitoringDisableSubscription, []string{"monitoring:write"}, false},
		{ActionMonitoringEnableSubscription, []string{"monitoring:write"}, false},
		{ActionMonitoringDeleteSubscription, []string{"monitoring:write"}, false},
		{ActionMonitoringRenameSubscription, []string{"monitoring:write"}, false},
		{ActionMonitoringPreview, []string{"monitoring:write"}, false},
		{ActionMonitoringEvaluationBacklog, []string{"plugins:admin"}, false},
		{ActionMonitoringRetireEvaluations, []string{"plugins:admin"}, false},
		{ActionMonitoringEvaluationRetirement, []string{"plugins:admin"}, false},
		{ActionOperationsRequestRebuild, []string{"projections:rebuild"}, false},
		{ActionOperationsConfigureRetrieval, []string{"corpora:write", "operations:write"}, false},
		{ActionOperationsRead, []string{"operations:read"}, false},
		{ActionPluginRegister, []string{"plugins:admin"}, false},
		{ActionPluginRegistrations, []string{"plugins:admin"}, false},
		{ActionPluginRegistration, []string{"plugins:admin"}, false},
		{ActionPluginActivePlan, []string{"plugins:admin"}, false},
		{ActionPluginActivePlugins, []string{"observability:read"}, true},
		{ActionPluginPipelinePlan, []string{"plugins:admin"}, false},
		{ActionPluginPipelinePlans, []string{"plugins:admin"}, false},
		{ActionPluginRollback, []string{"plugins:admin"}, false},
		{ActionPluginActivate, []string{"plugins:admin"}, false},
		{ActionRetrievalSearch, []string{"content:read", "search:query"}, false},
		{ActionQuarantineList, []string{"plugins:admin"}, false},
		{ActionQuarantineRequest, []string{"plugins:admin"}, false},
		{ActionUploadCreate, []string{"blobs:write"}, false},
		{ActionUploadRead, []string{"blobs:read"}, false},
		{ActionUploadConfirm, []string{"blobs:write"}, false},
		{ActionBlobRead, []string{"blobs:read"}, false},
		{ActionChangesStart, []string{"changes:read"}, false},
		{ActionChangesRead, []string{"changes:read"}, false},
		{ActionChangesPoll, []string{"changes:read"}, false},
		{ActionStatsRead, []string{"observability:read"}, true},
		{ActionSearchProfiles, []string{"search:query"}, false},
		{ActionVectorSpacesRead, []string{"corpora:read"}, false},
		{ActionConnectorDeliver, []string{"connector:push"}, false},
		{ActionConnectorCreateToken, []string{"connectors:admin"}, false},
		{ActionConnectorListToken, []string{"connectors:admin"}, false},
		{ActionConnectorRotateToken, []string{"connectors:admin"}, false},
		{ActionConnectorRevokeToken, []string{"connectors:admin"}, false},
		{ActionOperationCancel, []string{"operations:write"}, false},
		{ActionOperationPause, []string{"operations:write"}, false},
		{ActionOperationResume, []string{"operations:write"}, false},
		{ActionOperationRerun, []string{"operations:write"}, false},
		{ActionOperationPauseBackfill, []string{"operations:write", "plugins:admin"}, false},
		{ActionOperationPauseReprocess, []string{"operations:write", "plugins:admin"}, false},
		{ActionOperationResumeBackfill, []string{"operations:write", "plugins:admin"}, false},
		{ActionOperationResumeReprocess, []string{"operations:write", "plugins:admin"}, false},
		{ActionOperationRerunRebuild, []string{"operations:write", "projections:rebuild"}, false},
		{ActionOperationRerunRetrieval, []string{"operations:write", "corpora:write"}, false},
		{ActionOperationRerunBackfill, []string{"operations:write", "plugins:admin"}, false},
		{ActionOperationRerunReprocess, []string{"operations:write", "plugins:admin"}, false},
	}
	if len(cases) != len(actionRequirements) {
		t.Fatalf("policy has %d actions, table covers %d", len(actionRequirements), len(cases))
	}
	for _, tc := range cases {
		t.Run(string(tc.action), func(t *testing.T) {
			scope := Scope{Organization: "org", Actions: tc.grants, Corpora: []string{"*"}}
			if err := scope.Require(tc.action); err != nil {
				t.Fatalf("authorized action: %v", err)
			}
			for i := range tc.grants {
				denied := scope
				denied.Actions = append(append([]string{}, tc.grants[:i]...), tc.grants[i+1:]...)
				if err := denied.Require(tc.action); !errors.Is(err, ErrForbidden) {
					t.Fatalf("missing %s: got %v, want forbidden", tc.grants[i], err)
				}
			}
			scope.Corpora = []string{"corpus"}
			err := scope.Require(tc.action)
			if tc.allCorpora && !errors.Is(err, ErrForbidden) {
				t.Fatalf("restricted Corpus grant: got %v, want forbidden", err)
			}
			if !tc.allCorpora && err != nil {
				t.Fatalf("restricted Corpus grant: %v", err)
			}
		})
	}
	if err := (Scope{Actions: []string{"plugins:admin"}, Corpora: []string{"*"}}).Require(Action("unknown")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unknown action: %v", err)
	}
}
