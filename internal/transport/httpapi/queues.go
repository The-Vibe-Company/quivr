package httpapi

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"net/http"
	"time"
)

func WithQueues(reader workqueue.Reader) Option { return func(a *API) { a.Queues = reader } }
func (a *API) GetQueueBacklog(ctx context.Context, in transport.GetQueueBacklogRequestObject) (transport.GetQueueBacklogResponseObject, error) {
	return transport.GetQueueBacklogResponseFunc(func(w http.ResponseWriter) {
		if err := requestScope(ctx).Require(corpus.ActionQueuesRead); err != nil {
			writeError(w, err, nil)
			return
		}
		if a.Queues == nil {
			writeError(w, publicerr.NotFound, nil)
			return
		}
		read, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		rows, err := a.Queues.QueueBacklog(read)
		if err != nil {
			writeError(w, publicerr.StorageUnavailable, nil)
			return
		}
		queues := map[string]workqueue.Status{"live": {}, "bulk": {}}
		for _, row := range rows {
			queues[row.Queue] = row
		}
		send(w, 200, struct {
			Queues map[string]workqueue.Status `json:"queues"`
		}{queues})
	}), nil
}
