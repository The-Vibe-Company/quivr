package weaviate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// The gRPC methods the adapter calls.
const (
	searchMethod      = "/weaviate.v1.Weaviate/Search"
	batchInsertMethod = "/weaviate.v1.Weaviate/BatchObjects"
	batchDeleteMethod = "/weaviate.v1.Weaviate/BatchDelete"
)

// fakeMeta is what the client library reads on connect.
const fakeMeta = `{"version":"1.39.14","grpcMaxMessageSize":104858000}`

// fakeObject is one stored object: scalar properties and named vectors as
// they arrived on the wire.
type fakeObject struct {
	id         string
	properties map[string]*pb.Value
	vectors    []*pb.Vectors
}

// fakeIndex is an in-memory index on loopback: REST for the schema and
// single deletes, gRPC for search, insert and delete by filter. It stores
// what it is sent and selects by equality on text properties and on the
// object id; it ranks nothing. Batching, verification, retries and purge
// windows stay in the adapter under test.
type fakeIndex struct {
	*wire
	t *testing.T
	// Meta, set before the adapter connects, replaces fakeMeta.
	Meta string
	// DeleteCap, when positive, is the most objects one delete removes.
	DeleteCap int
	// Call, when set, answers a gRPC call. apply runs the index's own
	// answer, so a test can fail a call unsent, apply it and lose the
	// reply, or answer for the index.
	Call func(ctx context.Context, method string, request proto.Message, apply func() (proto.Message, error)) (proto.Message, error)

	mu         sync.Mutex
	objects    map[string]*fakeObject
	properties map[string]bool
}

func newFakeIndex(t *testing.T) *fakeIndex {
	t.Helper()
	f := &fakeIndex{t: t, objects: map[string]*fakeObject{}, properties: map[string]bool{}}
	f.wire = serve(t, f.rest, f.grpc)
	return f
}

// seed stores one object with text properties given as name/value pairs.
func (f *fakeIndex) seed(id string, pairs ...string) {
	object := &fakeObject{id: id, properties: map[string]*pb.Value{}}
	for i := 0; i < len(pairs); i += 2 {
		object.properties[pairs[i]] = &pb.Value{Kind: &pb.Value_TextValue{TextValue: pairs[i+1]}}
	}
	f.mu.Lock()
	f.objects[id] = object
	f.mu.Unlock()
}

// ids lists the stored object ids in order.
func (f *fakeIndex) ids() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for id := range f.objects {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// remove deletes one object, as another writer would.
func (f *fakeIndex) remove(id string) {
	f.mu.Lock()
	delete(f.objects, id)
	f.mu.Unlock()
}

// vectorIDs lists the stored objects that carry a vector.
func (f *fakeIndex) vectorIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, object := range f.objects {
		if len(object.vectors) > 0 {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeIndex) rest(w http.ResponseWriter, r *http.Request, _ wireRequest) {
	reply := func(code int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if value != nil {
			_ = json.NewEncoder(w).Encode(value)
		}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/meta":
		meta := f.Meta
		if meta == "" {
			meta = fakeMeta
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(meta))
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "schema":
		f.mu.Lock()
		properties := []any{}
		for name := range f.properties {
			properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}})
		}
		f.mu.Unlock()
		// The sections a real index always returns; the client library reads them all.
		reply(http.StatusOK, map[string]any{
			"class": parts[2], "properties": properties,
			"replicationConfig":   map[string]any{"factor": 1, "asyncConfig": map[string]any{}},
			"invertedIndexConfig": map[string]any{"bm25": map[string]any{}, "stopwords": map[string]any{}},
			"multiTenancyConfig":  map[string]any{},
			"objectTtlConfig":     map[string]any{},
		})
	case r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "schema" && parts[3] == "properties":
		var property struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&property); err != nil || property.Name == "" {
			reply(http.StatusUnprocessableEntity, nil)
			return
		}
		f.mu.Lock()
		f.properties[property.Name] = true
		f.mu.Unlock()
		reply(http.StatusOK, map[string]any{"name": property.Name, "dataType": []string{"text"}})
	case r.Method == http.MethodDelete && len(parts) == 4 && parts[1] == "objects":
		f.mu.Lock()
		_, found := f.objects[parts[3]]
		delete(f.objects, parts[3])
		f.mu.Unlock()
		if !found {
			reply(http.StatusNotFound, nil)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("fake index: unexpected REST request %s %s", r.Method, r.URL.Path)
		reply(http.StatusNotFound, nil)
	}
}

func (f *fakeIndex) grpc(ctx context.Context, request wireRequest, in []byte) ([]byte, error) {
	var message proto.Message
	var apply func() (proto.Message, error)
	switch request.Name {
	case searchMethod:
		search := &pb.SearchRequest{}
		message, apply = search, func() (proto.Message, error) { return f.search(search), nil }
	case batchInsertMethod:
		insert := &pb.BatchObjectsRequest{}
		message, apply = insert, func() (proto.Message, error) { return f.insert(insert), nil }
	case batchDeleteMethod:
		deletion := &pb.BatchDeleteRequest{}
		message, apply = deletion, func() (proto.Message, error) { return f.delete(deletion), nil }
	default:
		f.t.Errorf("fake index: unexpected gRPC call %s", request.Name)
		return nil, status.Error(codes.Unimplemented, request.Name)
	}
	if err := proto.Unmarshal(in, message); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var out proto.Message
	var err error
	if f.Call != nil {
		out, err = f.Call(ctx, request.Name, message, apply)
	} else {
		out, err = apply()
	}
	if err != nil {
		return nil, err
	}
	return proto.Marshal(out)
}

// matches evaluates the filters the adapter sends for identity, ownership
// and purge selection: And, Or and Equal on a text property or the id.
func (f *fakeIndex) matches(filter *pb.Filters, object *fakeObject) bool {
	if filter == nil {
		return true
	}
	switch filter.Operator {
	case pb.Filters_OPERATOR_AND:
		for _, operand := range filter.Filters {
			if !f.matches(operand, object) {
				return false
			}
		}
		return true
	case pb.Filters_OPERATOR_OR:
		for _, operand := range filter.Filters {
			if f.matches(operand, object) {
				return true
			}
		}
		return false
	case pb.Filters_OPERATOR_EQUAL:
		property := filter.GetTarget().GetProperty()
		if property == "_id" {
			return object.id == filter.GetValueText()
		}
		value, ok := object.properties[property]
		return ok && value.GetTextValue() == filter.GetValueText()
	}
	f.t.Errorf("fake index: unsupported filter operator %s", filter.Operator)
	return false
}

// selected lists the objects a filter matches, by id. Callers hold f.mu.
func (f *fakeIndex) selected(filter *pb.Filters) []*fakeObject {
	var out []*fakeObject
	for _, object := range f.objects {
		if f.matches(filter, object) {
			out = append(out, object)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (f *fakeIndex) search(request *pb.SearchRequest) *pb.SearchReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := f.selected(request.Filters)
	if limit := int(request.Limit); limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	reply := &pb.SearchReply{}
	for _, object := range rows {
		id := uuid.MustParse(object.id)
		metadata := &pb.MetadataResult{Id: object.id, IdAsBytes: id[:]}
		for _, name := range request.GetMetadata().GetVectors() {
			for _, vector := range object.vectors {
				if vector.Name == name {
					metadata.Vectors = append(metadata.Vectors, vector)
				}
			}
		}
		fields := map[string]*pb.Value{}
		if properties := request.GetProperties(); properties.GetReturnAllNonrefProperties() {
			fields = object.properties
		} else {
			for _, name := range properties.GetNonRefProperties() {
				if value, ok := object.properties[name]; ok {
					fields[name] = value
				}
			}
		}
		reply.Results = append(reply.Results, &pb.SearchResult{Metadata: metadata, Properties: &pb.PropertiesResult{TargetCollection: request.Collection, NonRefProps: &pb.Properties{Fields: fields}}})
	}
	return reply
}

func (f *fakeIndex) insert(request *pb.BatchObjectsRequest) *pb.BatchObjectsReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, incoming := range request.Objects {
		object := &fakeObject{id: incoming.Uuid, properties: map[string]*pb.Value{}, vectors: incoming.Vectors}
		for name, value := range incoming.GetProperties().GetNonRefProperties().GetFields() {
			text, ok := value.AsInterface().(string)
			if !ok {
				f.t.Errorf("fake index: property %s is not text", name)
				continue
			}
			object.properties[name] = &pb.Value{Kind: &pb.Value_TextValue{TextValue: text}}
		}
		f.objects[object.id] = object
	}
	return &pb.BatchObjectsReply{}
}

func (f *fakeIndex) delete(request *pb.BatchDeleteRequest) *pb.BatchDeleteReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := f.selected(request.Filters)
	if f.DeleteCap > 0 && len(rows) > f.DeleteCap {
		rows = rows[:f.DeleteCap]
	}
	reply := &pb.BatchDeleteReply{Matches: int64(len(rows))}
	if request.DryRun {
		return reply
	}
	for _, object := range rows {
		delete(f.objects, object.id)
		if request.Verbose {
			id := uuid.MustParse(object.id)
			reply.Objects = append(reply.Objects, &pb.BatchDeleteObject{Uuid: id[:], Successful: true})
		}
	}
	reply.Successful = int64(len(rows))
	return reply
}

// filterIDs lists the object ids a filter names.
func filterIDs(filter *pb.Filters) []string {
	var out []string
	if filter.GetTarget().GetProperty() == "_id" {
		out = append(out, filter.GetValueText())
	}
	for _, operand := range filter.GetFilters() {
		out = append(out, filterIDs(operand)...)
	}
	return out
}

// fences lists the text equalities every match of a filter must satisfy: the
// conditions at the top level or directly under its And.
func fences(filter *pb.Filters) map[string]string {
	out := map[string]string{}
	operands := []*pb.Filters{filter}
	if filter.GetOperator() == pb.Filters_OPERATOR_AND {
		operands = filter.Filters
	}
	for _, operand := range operands {
		if property := operand.GetTarget().GetProperty(); operand.GetOperator() == pb.Filters_OPERATOR_EQUAL && property != "" && property != "_id" {
			out[property] = operand.GetValueText()
		}
	}
	return out
}
