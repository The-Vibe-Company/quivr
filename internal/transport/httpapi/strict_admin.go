package httpapi

import (
	"context"
	"net/http"

	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

func (a *API) ListPluginRegistrations(ctx context.Context, in transport.ListPluginRegistrationsRequestObject) (transport.ListPluginRegistrationsResponseObject, error) {
	return transport.ListPluginRegistrationsResponseFunc(func(w http.ResponseWriter) { a.handleListPluginRegistrations(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) RegisterPlugin(ctx context.Context, in transport.RegisterPluginRequestObject) (transport.RegisterPluginResponseObject, error) {
	return transport.RegisterPluginResponseFunc(func(w http.ResponseWriter) { a.registerPlugin(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) GetPluginRegistration(ctx context.Context, in transport.GetPluginRegistrationRequestObject) (transport.GetPluginRegistrationResponseObject, error) {
	return transport.GetPluginRegistrationResponseFunc(func(w http.ResponseWriter) {
		a.handleGetPluginRegistration(w, in.HTTPRequest, requestScope(ctx), in.RegistrationId)
	}), nil
}

func (a *API) ActivatePlugin(ctx context.Context, in transport.ActivatePluginRequestObject) (transport.ActivatePluginResponseObject, error) {
	return transport.ActivatePluginResponseFunc(func(w http.ResponseWriter) {
		a.handleActivatePlugin(w, in.HTTPRequest, requestScope(ctx), in.RegistrationId)
	}), nil
}

func (a *API) GetPipelinePlan(ctx context.Context, in transport.GetPipelinePlanRequestObject) (transport.GetPipelinePlanResponseObject, error) {
	return transport.GetPipelinePlanResponseFunc(func(w http.ResponseWriter) { a.handleGetPipelinePlan(w, in.HTTPRequest, requestScope(ctx), in.PlanId) }), nil
}

func (a *API) ListPipelinePlans(ctx context.Context, in transport.ListPipelinePlansRequestObject) (transport.ListPipelinePlansResponseObject, error) {
	return transport.ListPipelinePlansResponseFunc(func(w http.ResponseWriter) { a.listPlans(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) RequestBackfill(ctx context.Context, in transport.RequestBackfillRequestObject) (transport.RequestBackfillResponseObject, error) {
	return transport.RequestBackfillResponseFunc(func(w http.ResponseWriter) { a.requestBackfill(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) PromoteVectorSpace(ctx context.Context, in transport.PromoteVectorSpaceRequestObject) (transport.PromoteVectorSpaceResponseObject, error) {
	return transport.PromoteVectorSpaceResponseFunc(func(w http.ResponseWriter) { a.promoteSpace(w, in.HTTPRequest, requestScope(ctx), in.VectorSpaceId) }), nil
}

func (a *API) ListQuarantinedVersions(ctx context.Context, in transport.ListQuarantinedVersionsRequestObject) (transport.ListQuarantinedVersionsResponseObject, error) {
	return transport.ListQuarantinedVersionsResponseFunc(func(w http.ResponseWriter) { a.listQuarantine(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) ReprocessQuarantine(ctx context.Context, in transport.ReprocessQuarantineRequestObject) (transport.ReprocessQuarantineResponseObject, error) {
	return transport.ReprocessQuarantineResponseFunc(func(w http.ResponseWriter) { a.requestReprocess(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) MigrateSubscriptionEvaluators(ctx context.Context, in transport.MigrateSubscriptionEvaluatorsRequestObject) (transport.MigrateSubscriptionEvaluatorsResponseObject, error) {
	return transport.MigrateSubscriptionEvaluatorsResponseFunc(func(w http.ResponseWriter) { a.migrateEvaluators(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) ListEvaluationBacklog(ctx context.Context, in transport.ListEvaluationBacklogRequestObject) (transport.ListEvaluationBacklogResponseObject, error) {
	return transport.ListEvaluationBacklogResponseFunc(func(w http.ResponseWriter) { a.handleListEvaluationBacklog(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) RetireEvaluations(ctx context.Context, in transport.RetireEvaluationsRequestObject) (transport.RetireEvaluationsResponseObject, error) {
	return transport.RetireEvaluationsResponseFunc(func(w http.ResponseWriter) { a.handleRetireEvaluations(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) GetEvaluationRetirement(ctx context.Context, in transport.GetEvaluationRetirementRequestObject) (transport.GetEvaluationRetirementResponseObject, error) {
	return transport.GetEvaluationRetirementResponseFunc(func(w http.ResponseWriter) {
		a.handleGetEvaluationRetirement(w, in.HTTPRequest, requestScope(ctx), in.RetirementId)
	}), nil
}

func (a *API) RollbackPipelinePlan(ctx context.Context, in transport.RollbackPipelinePlanRequestObject) (transport.RollbackPipelinePlanResponseObject, error) {
	return transport.RollbackPipelinePlanResponseFunc(func(w http.ResponseWriter) { a.rollbackPlan(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) GetActivePipelinePlan(ctx context.Context, in transport.GetActivePipelinePlanRequestObject) (transport.GetActivePipelinePlanResponseObject, error) {
	return transport.GetActivePipelinePlanResponseFunc(func(w http.ResponseWriter) { a.handleGetActivePipelinePlan(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) ListActivePlugins(ctx context.Context, in transport.ListActivePluginsRequestObject) (transport.ListActivePluginsResponseObject, error) {
	return transport.ListActivePluginsResponseFunc(func(w http.ResponseWriter) { a.handleListActivePlugins(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) ListAdminDocuments(ctx context.Context, in transport.ListAdminDocumentsRequestObject) (transport.ListAdminDocumentsResponseObject, error) {
	return transport.ListAdminDocumentsResponseFunc(func(w http.ResponseWriter) { a.listDocuments(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) GetDocumentTimeline(ctx context.Context, in transport.GetDocumentTimelineRequestObject) (transport.GetDocumentTimelineResponseObject, error) {
	return transport.GetDocumentTimelineResponseFunc(func(w http.ResponseWriter) {
		a.handleGetDocumentTimeline(w, in.HTTPRequest, requestScope(ctx), in.VersionId)
	}), nil
}

func (a *API) ListConnectorPushStats(ctx context.Context, in transport.ListConnectorPushStatsRequestObject) (transport.ListConnectorPushStatsResponseObject, error) {
	return transport.ListConnectorPushStatsResponseFunc(func(w http.ResponseWriter) {
		a.handleStats(w, in.HTTPRequest, requestScope(ctx), observability.SeriesConnectorPush)
	}), nil
}

func (a *API) GetPluginCallStats(ctx context.Context, in transport.GetPluginCallStatsRequestObject) (transport.GetPluginCallStatsResponseObject, error) {
	return transport.GetPluginCallStatsResponseFunc(func(w http.ResponseWriter) {
		a.handleStats(w, in.HTTPRequest, requestScope(ctx), observability.SeriesPluginCall)
	}), nil
}

func (a *API) GetSearchStats(ctx context.Context, in transport.GetSearchStatsRequestObject) (transport.GetSearchStatsResponseObject, error) {
	return transport.GetSearchStatsResponseFunc(func(w http.ResponseWriter) {
		a.handleStats(w, in.HTTPRequest, requestScope(ctx), observability.SeriesSearch)
	}), nil
}

func (a *API) GetStepStats(ctx context.Context, in transport.GetStepStatsRequestObject) (transport.GetStepStatsResponseObject, error) {
	return transport.GetStepStatsResponseFunc(func(w http.ResponseWriter) {
		a.handleStats(w, in.HTTPRequest, requestScope(ctx), observability.SeriesStep)
	}), nil
}

func (a *API) GetReceivedStats(ctx context.Context, in transport.GetReceivedStatsRequestObject) (transport.GetReceivedStatsResponseObject, error) {
	return transport.GetReceivedStatsResponseFunc(func(w http.ResponseWriter) {
		a.handleStats(w, in.HTTPRequest, requestScope(ctx), observability.SeriesReceived)
	}), nil
}

func (a *API) GetMatchStats(ctx context.Context, in transport.GetMatchStatsRequestObject) (transport.GetMatchStatsResponseObject, error) {
	return transport.GetMatchStatsResponseFunc(func(w http.ResponseWriter) {
		a.handleStats(w, in.HTTPRequest, requestScope(ctx), observability.SeriesMatch)
	}), nil
}

func (a *API) GetTopQueries(ctx context.Context, in transport.GetTopQueriesRequestObject) (transport.GetTopQueriesResponseObject, error) {
	return transport.GetTopQueriesResponseFunc(func(w http.ResponseWriter) {
		a.handleStats(w, in.HTTPRequest, requestScope(ctx), observability.SeriesSearchQuery)
	}), nil
}
