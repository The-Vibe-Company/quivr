package transport

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Run beside generated transport types; EXAMPLES points to examples.json.
func TestContractRoundTrips(t *testing.T) {
	data, err := os.ReadFile(os.Getenv("EXAMPLES"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Schema string
		Value        json.RawMessage
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var target any
			switch c.Schema {
			case "FacetRequest":
				target = &FacetRequest{}
			case "FacetResponse":
				target = &FacetResponse{}
			case "SearchRequest":
				target = &SearchRequest{}
			case "SearchResponse":
				target = &SearchResponse{}
			case "SearchProfileList":
				target = &SearchProfileList{}
			case "IngestCommand":
				target = &IngestCommand{}
			case "Receipt":
				target = &Receipt{}
			case "BatchRequest":
				target = &BatchRequest{}
			case "BatchResult":
				target = &BatchResult{}
			case "Upload":
				target = &Upload{}
			case "Version":
				target = &Version{}
			case "Operation":
				target = &Operation{}
			case "SavedQueryCreate":
				target = &SavedQueryCreate{}
			case "SavedQuery":
				target = &SavedQuery{}
			case "SavedQueryVersionCreate":
				target = &SavedQueryVersionCreate{}
			case "SubscriptionCreate":
				target = &SubscriptionCreate{}
			case "SubscriptionVersionCreate":
				target = &SubscriptionVersionCreate{}
			case "Subscription":
				target = &Subscription{}
			case "SubscriptionPage":
				target = &SubscriptionPage{}
			case "Match":
				target = &Match{}
			case "WebhookEvent":
				target = &WebhookEvent{}
			case "Delivery":
				target = &Delivery{}
			case "DeliveryAttemptPage":
				target = &DeliveryAttemptPage{}
			case "ChangeEvent":
				target = &ChangeEvent{}
			case "ConnectorCreate":
				target = &ConnectorCreate{}
			case "ConnectorToken":
				target = &ConnectorToken{}
			case "ConnectorTokenCreated":
				target = &ConnectorTokenCreated{}
			case "ConnectorTokenList":
				target = &ConnectorTokenList{}
			case "Connector":
				target = &Connector{}
			case "ConnectorKindCatalog":
				target = &ConnectorKindCatalog{}
			case "ScheduleChange":
				target = &ScheduleChange{}
			case "RenameRequest":
				target = &RenameRequest{}
			case "PluginRegistrationList":
				target = &PluginRegistrationList{}
			case "PipelinePlan":
				target = &PipelinePlan{}
			case "PluginRegistration":
				target = &PluginRegistration{}
			case "PluginRegistrationRequest":
				target = &PluginRegistrationRequest{}
			case "DocumentTimeline":
				target = &DocumentTimeline{}
			case "PluginCallStatsList":
				target = &PluginCallStatsList{}
			case "TopQueryList":
				target = &TopQueryList{}
			case "ReceivedStatsList":
				target = &ReceivedStatsList{}
			case "MatchStatsList":
				target = &MatchStatsList{}
			default:
				t.Fatalf("unhandled fixture schema %s", c.Schema)
			}
			if err := json.Unmarshal(c.Value, target); err != nil {
				t.Fatal(err)
			}
			if facets, ok := target.(*FacetResponse); ok {
				var wire struct {
					Items []struct {
						Buckets []struct{ Value any }
					}
				}
				if err := json.Unmarshal(c.Value, &wire); err != nil {
					t.Fatal(err)
				}
				for i, facet := range facets.Items {
					for j, bucket := range facet.Buckets {
						if want, numeric := wire.Items[i].Buckets[j].Value.(float64); numeric {
							got, err := bucket.Value.AsFacetBucketValue1()
							if err != nil || float64(got) != want {
								t.Fatalf("numeric bucket changed: got %v, want %v, error %v", got, want, err)
							}
						}
					}
				}
			}
			encoded, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			if err := json.Unmarshal(c.Value, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("JSON changed: %s", encoded)
			}
		})
	}
}
