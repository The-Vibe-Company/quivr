package httpapi

import (
	"context"
	"net/http"

	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

func (a *API) CreateSavedQuery(ctx context.Context, in transport.CreateSavedQueryRequestObject) (transport.CreateSavedQueryResponseObject, error) {
	return transport.CreateSavedQueryResponseFunc(func(w http.ResponseWriter) { a.handleCreateSavedQuery(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) GetSavedQuery(ctx context.Context, in transport.GetSavedQueryRequestObject) (transport.GetSavedQueryResponseObject, error) {
	return transport.GetSavedQueryResponseFunc(func(w http.ResponseWriter) {
		a.handleGetSavedQuery(w, in.HTTPRequest, requestScope(ctx), in.SavedQueryId)
	}), nil
}

func (a *API) GetSavedQueryVersion(ctx context.Context, in transport.GetSavedQueryVersionRequestObject) (transport.GetSavedQueryVersionResponseObject, error) {
	return transport.GetSavedQueryVersionResponseFunc(func(w http.ResponseWriter) {
		a.handleGetSavedQueryVersion(w, in.HTTPRequest, requestScope(ctx), in.SavedQueryId, in.VersionId)
	}), nil
}

func (a *API) CreateSavedQueryVersion(ctx context.Context, in transport.CreateSavedQueryVersionRequestObject) (transport.CreateSavedQueryVersionResponseObject, error) {
	return transport.CreateSavedQueryVersionResponseFunc(func(w http.ResponseWriter) {
		a.handleCreateSavedQueryVersion(w, in.HTTPRequest, requestScope(ctx), in.SavedQueryId)
	}), nil
}

func (a *API) DeleteSavedQuery(ctx context.Context, in transport.DeleteSavedQueryRequestObject) (transport.DeleteSavedQueryResponseObject, error) {
	return transport.DeleteSavedQueryResponseFunc(func(w http.ResponseWriter) {
		a.handleDeleteSavedQuery(w, in.HTTPRequest, requestScope(ctx), in.SavedQueryId)
	}), nil
}

func (a *API) RenameSavedQuery(ctx context.Context, in transport.RenameSavedQueryRequestObject) (transport.RenameSavedQueryResponseObject, error) {
	return transport.RenameSavedQueryResponseFunc(func(w http.ResponseWriter) {
		a.handleRenameSavedQuery(w, in.HTTPRequest, requestScope(ctx), in.SavedQueryId)
	}), nil
}

func (a *API) ListSubscriptions(ctx context.Context, in transport.ListSubscriptionsRequestObject) (transport.ListSubscriptionsResponseObject, error) {
	return transport.ListSubscriptionsResponseFunc(func(w http.ResponseWriter) { a.listSubscriptions(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) CreateSubscription(ctx context.Context, in transport.CreateSubscriptionRequestObject) (transport.CreateSubscriptionResponseObject, error) {
	return transport.CreateSubscriptionResponseFunc(func(w http.ResponseWriter) { a.handleCreateSubscription(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) GetSubscription(ctx context.Context, in transport.GetSubscriptionRequestObject) (transport.GetSubscriptionResponseObject, error) {
	return transport.GetSubscriptionResponseFunc(func(w http.ResponseWriter) {
		a.handleGetSubscription(w, in.HTTPRequest, requestScope(ctx), in.SubscriptionId)
	}), nil
}

func (a *API) GetSubscriptionVersion(ctx context.Context, in transport.GetSubscriptionVersionRequestObject) (transport.GetSubscriptionVersionResponseObject, error) {
	return transport.GetSubscriptionVersionResponseFunc(func(w http.ResponseWriter) {
		a.handleGetSubscriptionVersion(w, in.HTTPRequest, requestScope(ctx), in.SubscriptionId, in.VersionId)
	}), nil
}

func (a *API) CreateSubscriptionVersion(ctx context.Context, in transport.CreateSubscriptionVersionRequestObject) (transport.CreateSubscriptionVersionResponseObject, error) {
	return transport.CreateSubscriptionVersionResponseFunc(func(w http.ResponseWriter) {
		a.handleCreateSubscriptionVersion(w, in.HTTPRequest, requestScope(ctx), in.SubscriptionId)
	}), nil
}

func (a *API) DeleteSubscription(ctx context.Context, in transport.DeleteSubscriptionRequestObject) (transport.DeleteSubscriptionResponseObject, error) {
	return transport.DeleteSubscriptionResponseFunc(func(w http.ResponseWriter) {
		a.handleDeleteSubscription(w, in.HTTPRequest, requestScope(ctx), in.SubscriptionId)
	}), nil
}

func (a *API) DisableSubscription(ctx context.Context, in transport.DisableSubscriptionRequestObject) (transport.DisableSubscriptionResponseObject, error) {
	return transport.DisableSubscriptionResponseFunc(func(w http.ResponseWriter) {
		a.handleDisableSubscription(w, in.HTTPRequest, requestScope(ctx), in.SubscriptionId)
	}), nil
}

func (a *API) EnableSubscription(ctx context.Context, in transport.EnableSubscriptionRequestObject) (transport.EnableSubscriptionResponseObject, error) {
	return transport.EnableSubscriptionResponseFunc(func(w http.ResponseWriter) {
		a.handleEnableSubscription(w, in.HTTPRequest, requestScope(ctx), in.SubscriptionId)
	}), nil
}

func (a *API) PreviewSubscription(ctx context.Context, in transport.PreviewSubscriptionRequestObject) (transport.PreviewSubscriptionResponseObject, error) {
	return transport.PreviewSubscriptionResponseFunc(func(w http.ResponseWriter) { a.previewSubscription(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) RenameSubscription(ctx context.Context, in transport.RenameSubscriptionRequestObject) (transport.RenameSubscriptionResponseObject, error) {
	return transport.RenameSubscriptionResponseFunc(func(w http.ResponseWriter) {
		a.handleRenameSubscription(w, in.HTTPRequest, requestScope(ctx), in.SubscriptionId)
	}), nil
}

func (a *API) ListMatches(ctx context.Context, in transport.ListMatchesRequestObject) (transport.ListMatchesResponseObject, error) {
	return transport.ListMatchesResponseFunc(func(w http.ResponseWriter) { a.listMatches(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) GetMatch(ctx context.Context, in transport.GetMatchRequestObject) (transport.GetMatchResponseObject, error) {
	return transport.GetMatchResponseFunc(func(w http.ResponseWriter) { a.handleGetMatch(w, in.HTTPRequest, requestScope(ctx), in.MatchId) }), nil
}

func (a *API) GetDelivery(ctx context.Context, in transport.GetDeliveryRequestObject) (transport.GetDeliveryResponseObject, error) {
	return transport.GetDeliveryResponseFunc(func(w http.ResponseWriter) { a.handleGetDelivery(w, in.HTTPRequest, requestScope(ctx), in.DeliveryId) }), nil
}

func (a *API) ListDeliveryAttempts(ctx context.Context, in transport.ListDeliveryAttemptsRequestObject) (transport.ListDeliveryAttemptsResponseObject, error) {
	return transport.ListDeliveryAttemptsResponseFunc(func(w http.ResponseWriter) { a.listAttempts(w, in.HTTPRequest, requestScope(ctx), in.DeliveryId) }), nil
}
