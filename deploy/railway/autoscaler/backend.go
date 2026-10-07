// Package railway implements the autoscaler's replica backend using Railway's
// environment-scoped project token. It is deployment code, outside the engine.
package railway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/The-Vibe-Company/quivr/internal/autoscaling"
)

const endpoint = "https://backboard.railway.com/graphql/v2"

type Backend struct {
	Token, ServiceID, EnvironmentID string
	Client                          *http.Client
}

type response struct {
	Errors []json.RawMessage `json:"errors"`
	Data   struct {
		Instance *struct {
			Replicas *int `json:"numReplicas"`
		} `json:"serviceInstance"`
		Updated bool `json:"serviceInstanceUpdate"`
	} `json:"data"`
}

func (b Backend) call(ctx context.Context, query string, input map[string]int) (response, error) {
	var result response
	variables := map[string]any{"serviceId": b.ServiceID, "environmentId": b.EnvironmentID}
	if input != nil {
		variables["input"] = input
	}
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return result, errors.New("invalid Railway request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return result, errors.New("invalid Railway endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Project-Access-Token", b.Token)
	if err = autoscaling.RequestJSON(b.Client, req, &result); err != nil {
		return result, err
	}
	if len(result.Errors) != 0 {
		return result, errors.New("Railway GraphQL request rejected")
	}
	return result, nil
}

func (b Backend) Replicas(ctx context.Context) (int, error) {
	result, err := b.call(ctx, `query($serviceId:String!,$environmentId:String!){serviceInstance(serviceId:$serviceId,environmentId:$environmentId){numReplicas}}`, nil)
	if err != nil {
		return 0, err
	}
	if result.Data.Instance == nil || result.Data.Instance.Replicas == nil || *result.Data.Instance.Replicas < 0 {
		return 0, errors.New("Railway replica count missing or invalid")
	}
	return *result.Data.Instance.Replicas, nil
}

func (b Backend) SetReplicas(ctx context.Context, count int) error {
	if count < 1 {
		return errors.New("Railway target replicas must be positive")
	}
	result, err := b.call(ctx, `mutation($serviceId:String!,$environmentId:String!,$input:ServiceInstanceUpdateInput!){serviceInstanceUpdate(serviceId:$serviceId,environmentId:$environmentId,input:$input)}`, map[string]int{"numReplicas": count})
	if err != nil {
		return err
	}
	if !result.Data.Updated {
		return errors.New("Railway replica update not acknowledged")
	}
	return nil
}
