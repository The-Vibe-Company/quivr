package httpapi

import (
	"context"
	"net/http"

	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

func (a *API) IngestRecord(ctx context.Context, in transport.IngestRecordRequestObject) (transport.IngestRecordResponseObject, error) {
	return transport.IngestRecordResponseFunc(func(w http.ResponseWriter) { a.handleIngestRecord(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) ListRecords(ctx context.Context, in transport.ListRecordsRequestObject) (transport.ListRecordsResponseObject, error) {
	return transport.ListRecordsResponseFunc(func(w http.ResponseWriter) { a.listRecords(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) IngestBatch(ctx context.Context, in transport.IngestBatchRequestObject) (transport.IngestBatchResponseObject, error) {
	return transport.IngestBatchResponseFunc(func(w http.ResponseWriter) { a.handleIngestBatch(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) WithdrawRecord(ctx context.Context, in transport.WithdrawRecordRequestObject) (transport.WithdrawRecordResponseObject, error) {
	return transport.WithdrawRecordResponseFunc(func(w http.ResponseWriter) { a.handleWithdrawRecord(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) GetRecord(ctx context.Context, in transport.GetRecordRequestObject) (transport.GetRecordResponseObject, error) {
	return transport.GetRecordResponseFunc(func(w http.ResponseWriter) { a.handleGetRecord(w, in.HTTPRequest, requestScope(ctx), in.RecordId) }), nil
}

func (a *API) GetVersion(ctx context.Context, in transport.GetVersionRequestObject) (transport.GetVersionResponseObject, error) {
	return transport.GetVersionResponseFunc(func(w http.ResponseWriter) {
		a.handleGetVersion(w, in.HTTPRequest, requestScope(ctx), in.RecordId, in.VersionId)
	}), nil
}

func (a *API) GetReceipt(ctx context.Context, in transport.GetReceiptRequestObject) (transport.GetReceiptResponseObject, error) {
	return transport.GetReceiptResponseFunc(func(w http.ResponseWriter) { a.handleGetReceipt(w, in.HTTPRequest, requestScope(ctx), in.ReceiptId) }), nil
}

func (a *API) CreateUpload(ctx context.Context, in transport.CreateUploadRequestObject) (transport.CreateUploadResponseObject, error) {
	return transport.CreateUploadResponseFunc(func(w http.ResponseWriter) { a.handleCreateUpload(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) ConfirmUpload(ctx context.Context, in transport.ConfirmUploadRequestObject) (transport.ConfirmUploadResponseObject, error) {
	return transport.ConfirmUploadResponseFunc(func(w http.ResponseWriter) { a.handleConfirmUpload(w, in.HTTPRequest, requestScope(ctx), in.UploadId) }), nil
}

func (a *API) GetUpload(ctx context.Context, in transport.GetUploadRequestObject) (transport.GetUploadResponseObject, error) {
	return transport.GetUploadResponseFunc(func(w http.ResponseWriter) { a.handleGetUpload(w, in.HTTPRequest, requestScope(ctx), in.UploadId) }), nil
}

func (a *API) GetBlob(ctx context.Context, in transport.GetBlobRequestObject) (transport.GetBlobResponseObject, error) {
	return transport.GetBlobResponseFunc(func(w http.ResponseWriter) { a.handleGetBlob(w, in.HTTPRequest, requestScope(ctx), in.BlobId) }), nil
}
