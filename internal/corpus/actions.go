package corpus

// Action names a service operation, independently of the grants on an API key.
type Action string

const (
	ActionMonitoringAttempts                  Action = "monitoring.attempts"
	ActionMonitoringSubscriptionVersion       Action = "monitoring.subscriptionversion"
	ActionMonitoringSavedQueryVersion         Action = "monitoring.savedqueryversion"
	ActionContentBatch                        Action = "content.batch"
	ActionBackfillPromote                     Action = "backfill.promote"
	ActionBackfillRequest                     Action = "backfill.request"
	ActionConnectorsCreate                    Action = "connectors.create"
	ActionConnectorsRead                      Action = "connectors.read"
	ActionConnectorsList                      Action = "connectors.list"
	ActionConnectorsDisable                   Action = "connectors.disable"
	ActionConnectorsReplaceCredential         Action = "connectors.replace.credential"
	ActionConnectorsKinds                     Action = "connectors.kinds"
	ActionConnectorsChangeSchedule            Action = "connectors.change.schedule"
	ActionConnectorsRequestRun                Action = "connectors.request.run"
	ActionActivityLatest                      Action = "activity.latest"
	ActionActivityVersion                     Action = "activity.version"
	ActionContentAccept                       Action = "content.accept"
	ActionContentWithdraw                     Action = "content.withdraw"
	ActionContentReceipt                      Action = "content.receipt"
	ActionContentRecord                       Action = "content.record"
	ActionContentRecords                      Action = "content.records"
	ActionContentVersion                      Action = "content.version"
	ActionContentHydrate                      Action = "content.hydrate"
	ActionCorpusCreate                        Action = "corpus.create"
	ActionCorpusRead                          Action = "corpus.read"
	ActionCorpusList                          Action = "corpus.list"
	ActionMonitoringMatches                   Action = "monitoring.matches"
	ActionMonitoringMatch                     Action = "monitoring.match"
	ActionMonitoringDelivery                  Action = "monitoring.delivery"
	ActionMonitoringMigrateEvaluator          Action = "monitoring.migrate.evaluator"
	ActionMonitoringCreateSavedQuery          Action = "monitoring.create.saved.query"
	ActionMonitoringSavedQuery                Action = "monitoring.saved.query"
	ActionMonitoringCreateSavedQueryVersion   Action = "monitoring.create.saved.query.version"
	ActionMonitoringDeleteSavedQuery          Action = "monitoring.delete.saved.query"
	ActionMonitoringRenameSavedQuery          Action = "monitoring.rename.saved.query"
	ActionMonitoringCreateSubscription        Action = "monitoring.create.subscription"
	ActionMonitoringSubscriptions             Action = "monitoring.subscriptions"
	ActionMonitoringSubscription              Action = "monitoring.subscription"
	ActionMonitoringCreateSubscriptionVersion Action = "monitoring.create.subscription.version"
	ActionMonitoringDisableSubscription       Action = "monitoring.disable.subscription"
	ActionMonitoringEnableSubscription        Action = "monitoring.enable.subscription"
	ActionMonitoringDeleteSubscription        Action = "monitoring.delete.subscription"
	ActionMonitoringRenameSubscription        Action = "monitoring.rename.subscription"
	ActionMonitoringPreview                   Action = "monitoring.preview"
	ActionMonitoringEvaluationBacklog         Action = "monitoring.evaluation.backlog"
	ActionMonitoringRetireEvaluations         Action = "monitoring.retire.evaluations"
	ActionMonitoringEvaluationRetirement      Action = "monitoring.evaluation.retirement"
	ActionOperationsRequestRebuild            Action = "operations.request.rebuild"
	ActionOperationsConfigureRetrieval        Action = "operations.configure.retrieval"
	ActionOperationsRead                      Action = "operations.read"
	ActionPluginRegister                      Action = "plugin.register"
	ActionPluginRegistrations                 Action = "plugin.registrations"
	ActionPluginRegistration                  Action = "plugin.registration"
	ActionPluginActivePlan                    Action = "plugin.active.plan"
	ActionPluginActivePlugins                 Action = "plugin.active.plugins"
	ActionPluginPipelinePlan                  Action = "plugin.pipeline.plan"
	ActionPluginPipelinePlans                 Action = "plugin.pipeline.plans"
	ActionPluginRollback                      Action = "plugin.rollback"
	ActionPluginActivate                      Action = "plugin.activate"
	ActionRetrievalSearch                     Action = "retrieval.search"
	ActionQuarantineList                      Action = "quarantine.list"
	ActionQuarantineRequest                   Action = "quarantine.request"
	ActionUploadCreate                        Action = "upload.create"
	ActionUploadRead                          Action = "upload.read"
	ActionUploadConfirm                       Action = "upload.confirm"
	ActionBlobRead                            Action = "blob.read"
	ActionChangesStart                        Action = "changes.start"
	ActionChangesRead                         Action = "changes.read"
	ActionChangesPoll                         Action = "changes.poll"
	ActionStatsRead                           Action = "stats.read"
	ActionSearchProfiles                      Action = "search.profiles"
	ActionVectorSpacesRead                    Action = "vector.spaces.read"
	ActionConnectorDeliver                    Action = "connector.push"
	ActionConnectorCreateToken                Action = "connector.create.token"
	ActionConnectorListToken                  Action = "connector.list.token"
	ActionConnectorRotateToken                Action = "connector.rotate.token"
	ActionConnectorRevokeToken                Action = "connector.revoke.token"
	ActionOperationCancel                     Action = "operation.cancel"
	ActionOperationPause                      Action = "operation.pause"
	ActionOperationResume                     Action = "operation.resume"
	ActionOperationRerun                      Action = "operation.rerun"
	ActionOperationPauseBackfill              Action = "operation.pause.backfill"
	ActionOperationPauseReprocess             Action = "operation.pause.reprocess"
	ActionOperationResumeBackfill             Action = "operation.resume.backfill"
	ActionOperationResumeReprocess            Action = "operation.resume.reprocess"
	ActionOperationRerunRebuild               Action = "operation.rerun.rebuild"
	ActionOperationRerunRetrieval             Action = "operation.rerun.retrieval"
	ActionOperationRerunBackfill              Action = "operation.rerun.backfill"
	ActionOperationRerunReprocess             Action = "operation.rerun.reprocess"
)

type requirement struct {
	permissions []string
	allCorpora  bool
}

// actionRequirements is the single permission policy for service operations.
var actionRequirements = map[Action]requirement{
	ActionMonitoringAttempts:                  {permissions: []string{"monitoring:read"}},
	ActionMonitoringSubscriptionVersion:       {permissions: []string{"monitoring:read"}},
	ActionMonitoringSavedQueryVersion:         {permissions: []string{"monitoring:read"}},
	ActionContentBatch:                        {permissions: []string{"content:write"}},
	ActionBackfillPromote:                     {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionBackfillRequest:                     {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionConnectorsCreate:                    {permissions: []string{"connectors:write"}, allCorpora: false},
	ActionConnectorsRead:                      {permissions: []string{"connectors:read"}, allCorpora: false},
	ActionConnectorsList:                      {permissions: []string{"connectors:read"}, allCorpora: false},
	ActionConnectorsDisable:                   {permissions: []string{"connectors:write"}, allCorpora: false},
	ActionConnectorsReplaceCredential:         {permissions: []string{"connectors:write"}, allCorpora: false},
	ActionConnectorsKinds:                     {permissions: []string{"connectors:read"}, allCorpora: false},
	ActionConnectorsChangeSchedule:            {permissions: []string{"connectors:write"}, allCorpora: false},
	ActionConnectorsRequestRun:                {permissions: []string{"connectors:write"}, allCorpora: false},
	ActionActivityLatest:                      {permissions: []string{"observability:read"}, allCorpora: true},
	ActionActivityVersion:                     {permissions: []string{"observability:read"}, allCorpora: false},
	ActionContentAccept:                       {permissions: []string{"content:write"}, allCorpora: false},
	ActionContentWithdraw:                     {permissions: []string{"content:write"}, allCorpora: false},
	ActionContentReceipt:                      {permissions: []string{"content:read"}, allCorpora: false},
	ActionContentRecord:                       {permissions: []string{"content:read"}, allCorpora: false},
	ActionContentRecords:                      {permissions: []string{"content:read"}, allCorpora: false},
	ActionContentVersion:                      {permissions: []string{"content:read"}, allCorpora: false},
	ActionContentHydrate:                      {permissions: []string{"content:read", "search:query"}, allCorpora: false},
	ActionCorpusCreate:                        {permissions: []string{"corpora:write"}, allCorpora: true},
	ActionCorpusRead:                          {permissions: []string{"corpora:read"}, allCorpora: false},
	ActionCorpusList:                          {permissions: []string{"corpora:read"}, allCorpora: false},
	ActionMonitoringMatches:                   {permissions: []string{"monitoring:read"}, allCorpora: false},
	ActionMonitoringMatch:                     {permissions: []string{"monitoring:read"}, allCorpora: false},
	ActionMonitoringDelivery:                  {permissions: []string{"monitoring:read"}, allCorpora: false},
	ActionMonitoringMigrateEvaluator:          {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionMonitoringCreateSavedQuery:          {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringSavedQuery:                {permissions: []string{"monitoring:read"}, allCorpora: false},
	ActionMonitoringCreateSavedQueryVersion:   {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringDeleteSavedQuery:          {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringRenameSavedQuery:          {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringCreateSubscription:        {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringSubscriptions:             {permissions: []string{"monitoring:read"}, allCorpora: false},
	ActionMonitoringSubscription:              {permissions: []string{"monitoring:read"}, allCorpora: false},
	ActionMonitoringCreateSubscriptionVersion: {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringDisableSubscription:       {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringEnableSubscription:        {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringDeleteSubscription:        {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringRenameSubscription:        {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringPreview:                   {permissions: []string{"monitoring:write"}, allCorpora: false},
	ActionMonitoringEvaluationBacklog:         {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionMonitoringRetireEvaluations:         {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionMonitoringEvaluationRetirement:      {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionOperationsRequestRebuild:            {permissions: []string{"projections:rebuild"}, allCorpora: false},
	ActionOperationsConfigureRetrieval:        {permissions: []string{"corpora:write", "operations:write"}, allCorpora: false},
	ActionOperationsRead:                      {permissions: []string{"operations:read"}, allCorpora: false},
	ActionPluginRegister:                      {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionPluginRegistrations:                 {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionPluginRegistration:                  {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionPluginActivePlan:                    {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionPluginActivePlugins:                 {permissions: []string{"observability:read"}, allCorpora: true},
	ActionPluginPipelinePlan:                  {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionPluginPipelinePlans:                 {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionPluginRollback:                      {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionPluginActivate:                      {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionRetrievalSearch:                     {permissions: []string{"content:read", "search:query"}, allCorpora: false},
	ActionQuarantineList:                      {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionQuarantineRequest:                   {permissions: []string{"plugins:admin"}, allCorpora: false},
	ActionUploadCreate:                        {permissions: []string{"blobs:write"}, allCorpora: false},
	ActionUploadRead:                          {permissions: []string{"blobs:read"}, allCorpora: false},
	ActionUploadConfirm:                       {permissions: []string{"blobs:write"}, allCorpora: false},
	ActionBlobRead:                            {permissions: []string{"blobs:read"}, allCorpora: false},
	ActionChangesStart:                        {permissions: []string{"changes:read"}, allCorpora: false},
	ActionChangesRead:                         {permissions: []string{"changes:read"}, allCorpora: false},
	ActionChangesPoll:                         {permissions: []string{"changes:read"}, allCorpora: false},
	ActionStatsRead:                           {permissions: []string{"observability:read"}, allCorpora: true},
	ActionSearchProfiles:                      {permissions: []string{"search:query"}, allCorpora: false},
	ActionVectorSpacesRead:                    {permissions: []string{"corpora:read"}, allCorpora: false},
	ActionConnectorDeliver:                    {permissions: []string{"connector:push"}, allCorpora: false},
	ActionConnectorCreateToken:                {permissions: []string{"connectors:admin"}, allCorpora: false},
	ActionConnectorListToken:                  {permissions: []string{"connectors:admin"}, allCorpora: false},
	ActionConnectorRotateToken:                {permissions: []string{"connectors:admin"}, allCorpora: false},
	ActionConnectorRevokeToken:                {permissions: []string{"connectors:admin"}, allCorpora: false},
	ActionOperationCancel:                     {permissions: []string{"operations:write"}, allCorpora: false},
	ActionOperationPause:                      {permissions: []string{"operations:write"}, allCorpora: false},
	ActionOperationResume:                     {permissions: []string{"operations:write"}, allCorpora: false},
	ActionOperationRerun:                      {permissions: []string{"operations:write"}, allCorpora: false},
	ActionOperationPauseBackfill:              {permissions: []string{"operations:write", "plugins:admin"}, allCorpora: false},
	ActionOperationPauseReprocess:             {permissions: []string{"operations:write", "plugins:admin"}, allCorpora: false},
	ActionOperationResumeBackfill:             {permissions: []string{"operations:write", "plugins:admin"}, allCorpora: false},
	ActionOperationResumeReprocess:            {permissions: []string{"operations:write", "plugins:admin"}, allCorpora: false},
	ActionOperationRerunRebuild:               {permissions: []string{"operations:write", "projections:rebuild"}, allCorpora: false},
	ActionOperationRerunRetrieval:             {permissions: []string{"operations:write", "corpora:write"}, allCorpora: false},
	ActionOperationRerunBackfill:              {permissions: []string{"operations:write", "plugins:admin"}, allCorpora: false},
	ActionOperationRerunReprocess:             {permissions: []string{"operations:write", "plugins:admin"}, allCorpora: false},
}

// Require rejects unknown actions and missing grants. Resource visibility stays
// with the service so an inaccessible resource retains its not_found answer.
// A resolved action adds kind-specific permissions after a service has loaded
// the resource. Initial grants are checked first to preserve concealment.
func (s Scope) Require(action Action, resolve ...func() (Action, error)) error {
	checked := map[string]bool{}
	check := func(action Action, all bool) error {
		r, ok := actionRequirements[action]
		if !ok || (all && r.allCorpora && !s.AllCorpora()) {
			return ErrForbidden
		}
		for _, permission := range r.permissions {
			if !checked[permission] && !s.Allows(permission) {
				return ErrForbidden
			}
			checked[permission] = true
		}
		return nil
	}
	if err := check(action, len(resolve) == 0); err != nil {
		return err
	}
	for _, load := range resolve {
		next, err := load()
		if err != nil {
			return err
		}
		if err := check(next, true); err != nil {
			return err
		}
	}
	return check(action, true)
}
