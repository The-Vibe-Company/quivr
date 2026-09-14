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
			default:
				t.Fatalf("unhandled fixture schema %s", c.Schema)
			}
			if err := json.Unmarshal(c.Value, target); err != nil {
				t.Fatal(err)
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
