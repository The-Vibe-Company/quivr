package autoscaling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// NewHTTPClient bounds each API call and refuses redirects so credentials
// cannot be forwarded to another endpoint.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
}

// RequestJSON decodes a bounded response. Errors omit remote bodies and
// transport diagnostics, which can contain URLs or credentials.
func RequestJSON(client *http.Client, req *http.Request, out any) error {
	response, err := client.Do(req)
	if err != nil {
		return errors.New("API request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("API HTTP status %d", response.StatusCode)
	}
	const limit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(body) > limit {
		return errors.New("API response unreadable or too large")
	}
	if json.Unmarshal(body, out) != nil {
		return errors.New("invalid API JSON response")
	}
	return nil
}

type QueueSource struct {
	URL, Key string
	Client   *http.Client
}

func (q QueueSource) Waiting(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.URL, nil)
	if err != nil {
		return 0, errors.New("invalid queue URL")
	}
	req.Header.Set("Authorization", "Bearer "+q.Key)
	var response struct {
		Queues struct {
			Bulk struct {
				Waiting *int64 `json:"waiting"`
			} `json:"bulk"`
		} `json:"queues"`
	}
	if err = RequestJSON(q.Client, req, &response); err != nil {
		return 0, err
	}
	count := response.Queues.Bulk.Waiting
	if count == nil || *count < 0 {
		return 0, errors.New("bulk waiting count missing or invalid")
	}
	return *count, nil
}
