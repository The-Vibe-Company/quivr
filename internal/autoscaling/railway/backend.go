// Package railway implements the autoscaler's replica backend using Railway's
// environment-scoped project token.
package railway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

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
			Replicas         *int `json:"numReplicas"`
			LatestDeployment *struct {
				Meta struct {
					Manifest struct {
						Deploy struct {
							Regions  map[string]map[string]json.RawMessage `json:"multiRegionConfig"`
							Replicas *int                                  `json:"numReplicas"`
						} `json:"deploy"`
					} `json:"serviceManifest"`
				} `json:"meta"`
			} `json:"latestDeployment"`
		} `json:"serviceInstance"`
		Updated  bool `json:"serviceInstanceUpdate"`
		Deployed bool `json:"serviceInstanceDeploy"`
	} `json:"data"`
}

func (b Backend) call(ctx context.Context, query string, input map[string]any) (response, error) {
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
	req.Header.Set("User-Agent", "quivr-autoscaler")
	if err = autoscaling.RequestJSON(b.Client, req, &result); err != nil {
		return result, err
	}
	if len(result.Errors) != 0 {
		return result, errors.New("Railway GraphQL request rejected")
	}
	return result, nil
}

// Read deployment metadata because the plain setting can differ from the
// regional count that Railway actually deploys. A settings-only update must
// not conceal a deploy request that still needs retrying.
func (b Backend) read(ctx context.Context) (int, map[string]map[string]json.RawMessage, error) {
	result, err := b.call(ctx, `query($serviceId:String!,$environmentId:String!){serviceInstance(serviceId:$serviceId,environmentId:$environmentId){numReplicas latestDeployment{meta}}}`, nil)
	if err != nil {
		return 0, nil, err
	}
	instance := result.Data.Instance
	if instance == nil {
		return 0, nil, errors.New("Railway service instance missing")
	}
	if instance.LatestDeployment != nil {
		regions := instance.LatestDeployment.Meta.Manifest.Deploy.Regions
		if regions != nil {
			if len(regions) > 1 {
				return 0, nil, errors.New("Railway autoscaling requires a single-region service; multiple regions configured")
			}
			for name, config := range regions {
				// Railway represents a region scaled to zero as a null entry.
				if name != "" && config == nil {
					return 0, regions, nil
				}
				var replicas *int
				if name != "" && json.Unmarshal(config["numReplicas"], &replicas) == nil && replicas != nil && *replicas >= 0 && *replicas <= 1<<31-1 {
					return *replicas, regions, nil
				}
			}
			return 0, nil, errors.New("Railway regional replica count missing or invalid")
		}
		if deployed := instance.LatestDeployment.Meta.Manifest.Deploy.Replicas; deployed != nil {
			instance.Replicas = deployed
		}
	}
	if instance.Replicas == nil || *instance.Replicas < 0 || *instance.Replicas > 1<<31-1 {
		return 0, nil, errors.New("Railway replica count missing or invalid")
	}
	return *instance.Replicas, nil, nil
}

func (b Backend) Replicas(ctx context.Context) (int, error) {
	count, _, err := b.read(ctx)
	return count, err
}

func (b Backend) SetReplicas(ctx context.Context, count int) error {
	if count < 1 || count > 1<<31-1 {
		return errors.New("Railway target replicas must be positive GraphQL integers")
	}
	_, regions, err := b.read(ctx)
	if err != nil {
		return err
	}
	input := map[string]any{"numReplicas": count}
	if regions != nil {
		for name, config := range regions {
			if config == nil {
				config = make(map[string]json.RawMessage)
				regions[name] = config
			}
			config["numReplicas"] = json.RawMessage(strconv.Itoa(count))
		}
		input = map[string]any{"multiRegionConfig": regions}
	}
	result, err := b.call(ctx, `mutation($serviceId:String!,$environmentId:String!,$input:ServiceInstanceUpdateInput!){serviceInstanceUpdate(serviceId:$serviceId,environmentId:$environmentId,input:$input)}`, input)
	if err != nil {
		return err
	}
	if !result.Data.Updated {
		return errors.New("Railway replica update not acknowledged")
	}
	result, err = b.call(ctx, `mutation($serviceId:String!,$environmentId:String!){serviceInstanceDeploy(serviceId:$serviceId,environmentId:$environmentId)}`, nil)
	if err != nil {
		return err
	}
	if !result.Data.Deployed {
		return errors.New("Railway replica deployment not acknowledged")
	}
	return nil
}
