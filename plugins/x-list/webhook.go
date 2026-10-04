package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

// signatureHeader carries X's signature of a webhook delivery:
// "sha256=" + base64(HMAC-SHA256(consumer secret, raw body)).
const signatureHeader = "x-twitter-webhooks-signature"

// event is one Filtered Stream post delivered to the webhook.
type event struct {
	Data          *Post    `json:"data"`
	Includes      Includes `json:"includes"`
	MatchingRules []struct {
		ID  string `json:"id"`
		Tag string `json:"tag"`
	} `json:"matching_rules"`
}

// sign is X's HMAC-SHA256 of message under the consumer secret, base64 with
// the sha256= prefix, used for the CRC answer and delivery signatures.
func sign(secret string, message []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(message)
	return "sha256=" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Receive answers X's CRC check and turns signed Filtered Stream deliveries
// into items with MapPost, the mapping pull runs use, so a post both paths
// see converges on the same Receipt. An unsigned or wrongly signed delivery
// is refused and changes nothing.
func (XList) Receive(_ context.Context, req *quivrplugin.ReceiveRequest) (*quivrplugin.Delivery, error) {
	var cfg config
	if err := req.Connector.DecodeConfig(&cfg); err != nil {
		return nil, quivrplugin.SourceError("invalid_config", "the instance configuration cannot be read")
	}
	if !cfg.Webhook.Enabled {
		return quivrplugin.Refuse(http.StatusNotFound, "webhook mode is off for this instance"), nil
	}
	var secret struct {
		ConsumerSecret string `json:"consumer_secret"`
	}
	if req.Credential.Decode(&secret) != nil || secret.ConsumerSecret == "" {
		// Without the consumer secret no delivery can be verified.
		return nil, quivrplugin.AccessError("consumer_secret_missing", "the credential has no consumer_secret; deposit it to receive webhook deliveries")
	}
	switch req.Request.Method {
	case http.MethodGet:
		token := req.Request.QueryValues().Get("crc_token")
		if token == "" {
			return quivrplugin.Refuse(http.StatusBadRequest, "crc_token is missing"), nil
		}
		body, _ := json.Marshal(map[string]string{"response_token": sign(secret.ConsumerSecret, []byte(token))})
		return quivrplugin.Respond(http.StatusOK, "application/json", string(body)), nil
	case http.MethodPost:
		signature := req.Request.Header(signatureHeader)
		if signature == "" || !hmac.Equal([]byte(signature), []byte(sign(secret.ConsumerSecret, req.Request.Body()))) {
			return quivrplugin.Refuse(http.StatusUnauthorized, "invalid signature"), nil
		}
		events, ok := decodeEvents(req.Request.Body())
		if !ok {
			return quivrplugin.Refuse(http.StatusBadRequest, "unreadable delivery"), nil
		}
		tag := ruleTag(req.Connector.ID)
		scope := Scope{CorpusID: req.Connector.CorpusID, Namespace: req.Connector.SourceNamespace}
		seen := map[string]int{}
		var items []quivrplugin.Item
		for _, e := range events {
			if e.Data == nil || e.Data.ID == "" || !matches(e, tag) {
				continue
			}
			item := MapPost(*e.Data, e.Includes, scope)
			// Two versions of one post in a delivery: keep the latest.
			if i, dup := seen[item.RecordKey]; dup {
				if idAfter(item.Revision, items[i].Revision) {
					items[i] = item
				}
				continue
			}
			seen[item.RecordKey] = len(items)
			items = append(items, item)
		}
		d := quivrplugin.Accept(items...)
		d.Reads = int64(len(items))
		return d, nil
	}
	return quivrplugin.Refuse(http.StatusMethodNotAllowed, "only GET and POST"), nil
}

// matches keeps an event whose matching rules name this instance, or one
// without matching rules (X omits them only when there is no filter).
func matches(e event, tag string) bool {
	if len(e.MatchingRules) == 0 {
		return true
	}
	for _, r := range e.MatchingRules {
		if r.Tag == tag {
			return true
		}
	}
	return false
}

// decodeEvents reads one event or a JSON array of events.
func decodeEvents(body []byte) ([]event, bool) {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var events []event
		return events, json.Unmarshal(body, &events) == nil
	}
	var e event
	if json.Unmarshal(body, &e) != nil {
		return nil, false
	}
	return []event{e}, true
}
