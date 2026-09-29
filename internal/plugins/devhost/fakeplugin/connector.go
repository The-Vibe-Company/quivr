package fakeplugin

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// connectorRoutes serves the connector Contribution as a static source: the
// Connector Instance config lists the items ({"items": [{"key", "text"}],
// "page_size": n}), the checkpoint is {"offset": n}, and a credential token
// starting with "revoked" is refused with an access error. The other modes
// are the broken connector plugins the Contract Runner must reject:
// connector-credential-leak (logs the credential), connector-stalled-checkpoint
// (more: true without moving the checkpoint), connector-ignores-checkpoint
// (every run restarts from the first item), connector-wrong-error-class (an
// access error marked retryable), // connector-blob-part (a Blob Part in a
// Manifest), connector-too-many-items (one item over max_items),
// connector-attachment-mismatch (uploads other bytes than it described) and
// accept-invalid (invalid requests answered 202) and
// connector-receive-invalid-verdict (a refused delivery that carries items).
// A push kind's receive (Plugin API 0.5) answers GET ?challenge=<c> with <c>,
// and accepts a POST whose x-fake-signature is the hex HMAC-SHA256 of the body
// under the credential token, its body listing items like the config
// ({"items": [{"key", "text"}]}); any other signature is refused 401. An item with an
// "attachment" text carries it as a text/plain attachment whose ref is the
// item key, described and uploaded through the core's grant (Plugin API 0.4).
func connectorRoutes(mux *http.ServeMux, mode string, m *plugins.Manifest, write func(http.ResponseWriter, int, any)) {
	type request struct {
		Connector struct {
			Kind   string `json:"kind"`
			Config struct {
				Items []struct {
					Key        string `json:"key"`
					Text       string `json:"text"`
					Attachment string `json:"attachment"`
				} `json:"items"`
				PageSize int `json:"page_size"`
			} `json:"config"`
		} `json:"connector"`
		Credential *struct {
			Token string `json:"token"`
		} `json:"credential"`
		Checkpoint *struct {
			Offset int `json:"offset"`
		} `json:"checkpoint"`
		PageInRun int `json:"page_in_run"`
		Request   struct {
			Method     string              `json:"method"`
			Query      string              `json:"query"`
			Headers    map[string][]string `json:"headers"`
			BodyBase64 string              `json:"body_base64"`
		} `json:"request"`
		Attachment struct {
			Ref string `json:"ref"`
		} `json:"attachment"`
		Grant *struct {
			URL       string            `json:"url"`
			Headers   map[string]string `json:"headers"`
			SizeBytes int64             `json:"size_bytes"`
		} `json:"grant"`
	}
	// read decodes and validates a request; it answers the refusal itself and
	// returns false when the request is invalid or the credential is refused.
	read := func(w http.ResponseWriter, r *http.Request, schema string) (request, bool) {
		var req request
		body, _ := io.ReadAll(r.Body)
		var invalid error
		if issues := plugins.ValidateDocument(schema, body); len(issues) > 0 {
			invalid = fmt.Errorf("%s %s", issues[0].Path, issues[0].Message)
		} else if err := json.Unmarshal(body, &req); err != nil {
			invalid = err
		} else if _, ok := m.Contributions.Connector.Kinds[req.Connector.Kind]; !ok {
			invalid = fmt.Errorf("unknown kind %q", req.Connector.Kind)
		}
		if invalid != nil {
			status := 400
			if mode == "accept-invalid" {
				status = 202
			}
			write(w, status, map[string]any{"code": "invalid_request", "message": invalid.Error(), "retryable": false})
			return req, false
		}
		if mode == "connector-credential-leak" && req.Credential != nil {
			fmt.Fprintln(os.Stderr, "fetching with token", req.Credential.Token)
		}
		if req.Credential != nil && strings.HasPrefix(req.Credential.Token, "revoked") {
			write(w, 403, map[string]any{"code": "token_rejected", "message": "the source refused the token", "retryable": mode == "connector-wrong-error-class", "error_class": "access"})
			return req, false
		}
		return req, true
	}
	mux.HandleFunc("POST /v0/contributions/connector/check_credential", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := read(w, r, "connector-check-credential-request.schema.json"); ok {
			write(w, 200, map[string]any{"status": "ok"})
		}
	})
	mux.HandleFunc("POST /v0/contributions/connector/fetch", func(w http.ResponseWriter, r *http.Request) {
		req, ok := read(w, r, "connector-fetch-request.schema.json")
		if !ok {
			return
		}
		config := req.Connector.Config
		size := max(config.PageSize, 1)
		offset := 0
		if req.Checkpoint != nil && !(mode == "connector-ignores-checkpoint" && req.PageInRun == 0) {
			offset = req.Checkpoint.Offset
		}
		end := min(offset+size, len(config.Items))
		if mode == "connector-too-many-items" {
			end = min(offset+plugins.ConnectorMaxItems(m)+1, len(config.Items))
		}
		items := []any{}
		for _, item := range config.Items[min(offset, end):end] {
			if item.Attachment != "" {
				items = append(items, map[string]any{"record_key": item.Key, "revision": "1",
					"content":     map[string]any{"kind": "manifest", "parts": []any{map[string]any{"key": "body", "role": "body", "content": map[string]any{"kind": "text", "text": item.Text}}}},
					"attachments": []any{map[string]any{"key": "file", "role": "attachment", "media_type": "text/plain", "ref": item.Key}}})
				continue
			}
			items = append(items, map[string]any{"record_key": item.Key, "revision": "1", "content": map[string]any{"kind": "text", "text": item.Text}})
		}
		if mode == "connector-blob-part" && len(items) > 0 {
			items[0] = map[string]any{"record_key": config.Items[offset].Key, "content": map[string]any{"kind": "manifest", "parts": []any{
				map[string]any{"key": "photo", "role": "attachment", "content": map[string]any{"kind": "blob", "blob_id": "blob-1", "media_type": "image/jpeg"}},
			}}}
		}
		next := map[string]any{"offset": end}
		if mode == "connector-stalled-checkpoint" {
			next = map[string]any{"offset": offset}
		}
		write(w, 200, map[string]any{"items": items, "checkpoint": next, "more": end < len(config.Items), "reads": len(items)})
	})
	mux.HandleFunc("POST /v0/contributions/connector/receive", func(w http.ResponseWriter, r *http.Request) {
		req, ok := read(w, r, "connector-receive-request.schema.json")
		if !ok {
			return
		}
		if req.Request.Method == "GET" {
			challenge := strings.TrimPrefix(req.Request.Query, "challenge=")
			write(w, 200, map[string]any{"verdict": "accepted", "response": map[string]any{"status": 200, "content_type": "text/plain", "body": challenge}})
			return
		}
		body, _ := base64.StdEncoding.DecodeString(req.Request.BodyBase64)
		mac := hmac.New(sha256.New, []byte(req.Credential.Token))
		mac.Write(body)
		signature := req.Request.Headers["x-fake-signature"]
		var delivered struct {
			Items []struct {
				Key  string `json:"key"`
				Text string `json:"text"`
			} `json:"items"`
		}
		_ = json.Unmarshal(body, &delivered)
		items := []any{}
		for _, item := range delivered.Items {
			items = append(items, map[string]any{"record_key": item.Key, "revision": "1", "content": map[string]any{"kind": "text", "text": item.Text}})
		}
		if len(signature) != 1 || !hmac.Equal([]byte(signature[0]), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			refused := map[string]any{"verdict": "refused", "response": map[string]any{"status": 401, "body": "bad signature"}}
			if mode == "connector-receive-invalid-verdict" {
				refused["items"] = items
			}
			write(w, 200, refused)
			return
		}
		write(w, 200, map[string]any{"verdict": "accepted", "response": map[string]any{"status": 204}, "items": items, "reads": len(items)})
	})
	if m.Contributions.Connector.Attachments == nil {
		return
	}
	attachment := func(req request) []byte {
		for _, item := range req.Connector.Config.Items {
			if item.Key == req.Attachment.Ref {
				return []byte(item.Attachment)
			}
		}
		return nil
	}
	mux.HandleFunc("POST /v0/contributions/connector/describe_attachment", func(w http.ResponseWriter, r *http.Request) {
		if req, ok := read(w, r, "connector-describe-attachment-request.schema.json"); ok {
			b := attachment(req)
			sum := sha256.Sum256(b)
			write(w, 200, map[string]any{"size_bytes": len(b), "sha256": hex.EncodeToString(sum[:])})
		}
	})
	mux.HandleFunc("POST /v0/contributions/connector/upload_attachment", func(w http.ResponseWriter, r *http.Request) {
		req, ok := read(w, r, "connector-upload-attachment-request.schema.json")
		if !ok {
			return
		}
		b := attachment(req)
		if mode == "connector-attachment-mismatch" && len(b) > 0 {
			b = append(bytes.ToUpper(b[:1]), b[1:]...)
		}
		put, _ := http.NewRequest(http.MethodPut, req.Grant.URL, bytes.NewReader(b))
		for name, value := range req.Grant.Headers {
			put.Header.Set(name, value)
		}
		resp, err := http.DefaultClient.Do(put)
		if err != nil {
			write(w, 503, map[string]any{"code": "upload_unavailable", "message": "storage did not answer", "retryable": true, "error_class": "transient"})
			return
		}
		resp.Body.Close()
		write(w, 200, map[string]any{"status": "uploaded"})
	})
}
