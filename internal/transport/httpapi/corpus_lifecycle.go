package httpapi

import (
	"context"
	"net/http"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

func (a *API) listCorporaRequest(ctx context.Context, s corpus.Scope, r *http.Request, prepare func() (string, int, error)) ([]corpus.Corpus, error) {
	if err := s.Require(corpus.ActionCorpusList); err != nil {
		return nil, err
	}
	raw := r.URL.Query().Get("include_archived")
	if r.URL.Query().Has("include_archived") && raw != "true" && raw != "false" {
		return nil, publicerr.InvalidQuery
	}
	after, limit, err := prepare()
	if err != nil {
		return nil, err
	}
	return a.Service.ListArchived(ctx, s, after, limit, raw == "true")
}
func (a *API) ArchiveCorpus(ctx context.Context, in transport.ArchiveCorpusRequestObject) (transport.ArchiveCorpusResponseObject, error) {
	return transport.ArchiveCorpusResponseFunc(func(w http.ResponseWriter) { a.archiveCorpus(w, in.HTTPRequest, requestScope(ctx), in.CorpusId, true) }), nil
}
func (a *API) UnarchiveCorpus(ctx context.Context, in transport.UnarchiveCorpusRequestObject) (transport.UnarchiveCorpusResponseObject, error) {
	return transport.UnarchiveCorpusResponseFunc(func(w http.ResponseWriter) { a.archiveCorpus(w, in.HTTPRequest, requestScope(ctx), in.CorpusId, false) }), nil
}
func (a *API) archiveCorpus(w http.ResponseWriter, r *http.Request, s corpus.Scope, id string, archived bool) {
	c, err := a.Service.Archive(r.Context(), s, id, archived)
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	send(w, 200, c)
}
func (a *API) RenameCorpus(ctx context.Context, in transport.RenameCorpusRequestObject) (transport.RenameCorpusResponseObject, error) {
	return transport.RenameCorpusResponseFunc(func(w http.ResponseWriter) {
		scope := requestScope(ctx)
		if err := scope.Require(corpus.ActionCorpusRename); err != nil {
			writeError(w, err, nil)
			return
		}
		if !scope.Contains(in.CorpusId) {
			writeError(w, publicerr.NotFound, nil)
			return
		}
		var body transport.CorpusRenameRequest
		if !decodeInto(w, in.HTTPRequest, a.schemas["CorpusRenameRequest"], &body) {
			return
		}
		c, err := a.Service.Rename(ctx, scope, in.CorpusId, body.Name)
		if err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
			return
		}
		send(w, 200, c)
	}), nil
}
