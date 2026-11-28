package weaviate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// grpcDecodeRowHook is nil in production; white-box tests may install a hook
// to inject a decode-path panic and verify recovery to retrieval.ErrUnavailable.
var grpcDecodeRowHook atomic.Pointer[func(*pb.SearchResult)]

func (s *Store) grpcPooled() *grpcPooledClient {
	if pooled, ok := s.grpc.(*grpcPooledClient); ok {
		return pooled
	}
	return nil
}

func (s *Store) grpcBreaker() *circuitBreaker {
	if pooled := s.grpcPooled(); pooled != nil {
		return pooled.breaker
	}
	return nil
}

func (s *Store) rpcContext(ctx context.Context, operation string, callerDeadline bool) (context.Context, context.CancelFunc) {
	return s.rpcContextTimeout(ctx, operation, callerDeadline, s.Client.Timeout)
}

func (s *Store) rpcContextTimeout(ctx context.Context, operation string, callerDeadline bool, timeout time.Duration) (context.Context, context.CancelFunc) {
	if metrics := s.metrics.Load(); metrics != nil {
		metrics.Record(telemetry.DependencyWeaviate, telemetry.ProtocolGRPC, operation)
	}
	if s.apiKey != "" {
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			if pooled := s.grpcPooled(); pooled != nil && pooled.authMD != nil {
				ctx = metadata.NewOutgoingContext(ctx, pooled.authMD)
			} else {
				ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+s.apiKey))
			}
		} else {
			md = md.Copy()
			md.Set("authorization", "Bearer "+s.apiKey)
			ctx = metadata.NewOutgoingContext(ctx, md)
		}
	}
	if _, bounded := ctx.Deadline(); callerDeadline && bounded || timeout == 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func vectorMessage(name string, vector []float32) (*pb.Vectors, error) {
	raw, err := content.VectorBytes(vector)
	if err != nil {
		return nil, err
	}
	return &pb.Vectors{Name: name, VectorBytes: raw, Type: pb.Vectors_VECTOR_TYPE_SINGLE_FP32}, nil
}

func batchObject(object projectionObject) (*pb.BatchObject, error) {
	props := maps.Clone(object.Properties)
	out := &pb.BatchObject{Collection: object.Class, Tenant: object.Tenant, Uuid: object.ID, Properties: &pb.BatchObject_Properties{}}
	for name, value := range props {
		if values, ok := value.([]string); ok {
			if len(values) == 0 {
				out.Properties.EmptyListProps = append(out.Properties.EmptyListProps, name)
			} else {
				out.Properties.TextArrayProperties = append(out.Properties.TextArrayProperties, &pb.TextArrayProperties{PropName: name, Values: values})
			}
			delete(props, name)
		}
	}
	var err error
	out.Properties.NonRefProperties, err = structpb.NewStruct(props)
	if err != nil {
		return nil, err
	}
	if len(object.Vectors) > 0 {
		out.Vectors = make([]*pb.Vectors, 0, len(object.Vectors))
	}
	for name, vector := range object.Vectors {
		if raw, ok := object.encoded[name]; ok {
			out.Vectors = append(out.Vectors, &pb.Vectors{Name: name, VectorBytes: raw, Type: pb.Vectors_VECTOR_TYPE_SINGLE_FP32})
			continue
		}
		packed, err := vectorMessage(name, vector)
		if err != nil {
			return nil, err
		}
		out.Vectors = append(out.Vectors, packed)
	}
	return out, nil
}

// grpcBatchAttempts bounds the sends of one batch: the first send, then
// re-sends of only the objects the provider rejected in its reply.
const grpcBatchAttempts = 3

var errBatchUnacknowledged = errors.New("projection batch unacknowledged")

// grpcInsertBatch re-sends only the objects a reply rejects and reconciles a
// lost reply by reading back the objects of the send that lost it.
func (s *Store) grpcInsertBatch(ctx context.Context, objects []projectionObject) error {
	packed := make([]*pb.BatchObject, len(objects))
	for i, object := range objects {
		message, err := batchObject(object)
		if err != nil {
			return errors.New("invalid projection batch object")
		}
		packed[i] = message
	}
	for attempt := 1; ; attempt++ {
		rejected, err := s.grpcSendBatch(ctx, packed)
		if errors.Is(err, errBatchUnacknowledged) {
			return s.verifyBatch(ctx, objects)
		}
		if err != nil {
			return err
		}
		if len(rejected) == 0 {
			return nil
		}
		if attempt == grpcBatchAttempts {
			// Provider messages can quote request data; report only the count.
			return fmt.Errorf("projection publication failed (%d objects rejected)", len(rejected))
		}
		retryObjects := make([]projectionObject, len(rejected))
		retryPacked := make([]*pb.BatchObject, len(rejected))
		for j, i := range rejected {
			retryObjects[j], retryPacked[j] = objects[i], packed[i]
		}
		objects, packed = retryObjects, retryPacked
	}
}

// batchSize estimates the encoded size of a batch from what dominates it,
// vector payloads and property text, without walking it through proto.Size.
func batchSize(objects []*pb.BatchObject) int {
	size := 0
	for _, object := range objects {
		size += len(object.GetUuid()) + len(object.GetCollection()) + len(object.GetTenant())
		for _, vector := range object.GetVectors() {
			size += len(vector.GetName()) + len(vector.GetVectorBytes())
		}
		for _, list := range object.GetProperties().GetTextArrayProperties() {
			size += len(list.GetPropName())
			for _, value := range list.GetValues() {
				size += len(value)
			}
		}
		size += structSize(object.GetProperties().GetNonRefProperties())
	}
	return size
}

func structSize(value *structpb.Struct) int {
	size := 0
	for key, field := range value.GetFields() {
		size += len(key) + valueSize(field)
	}
	return size
}

func valueSize(value *structpb.Value) int {
	switch kind := value.GetKind().(type) {
	case *structpb.Value_StringValue:
		return len(kind.StringValue)
	case *structpb.Value_StructValue:
		return structSize(kind.StructValue)
	case *structpb.Value_ListValue:
		size := 0
		for _, item := range kind.ListValue.GetValues() {
			size += valueSize(item)
		}
		return size
	default:
		return 8
	}
}

// grpcSendBatch sends one batch under a deadline sized to its encoding and
// returns the ascending, distinct indexes of the objects the reply rejects.
func (s *Store) grpcSendBatch(ctx context.Context, objects []*pb.BatchObject) ([]int, error) {
	request := &pb.BatchObjectsRequest{Objects: objects}
	rpcCtx, cancel := s.rpcContextTimeout(ctx, telemetry.OpWrite, false, s.writeTimeout(batchSize(objects)))
	defer cancel()
	reply, err := s.grpc.BatchObjects(rpcCtx, request)
	if err != nil {
		switch status.Code(err) {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unknown, codes.Internal:
			return nil, errBatchUnacknowledged
		default:
			return nil, fmt.Errorf("projection batch request failed (gRPC %s)", status.Code(err))
		}
	}
	replyErrors := reply.GetErrors()
	if len(replyErrors) == 0 {
		return nil, nil
	}
	failed := make([]bool, len(objects))
	for _, rejected := range replyErrors {
		i := int(rejected.GetIndex())
		if i < 0 || i >= len(objects) {
			return nil, errors.New("projection publication response invalid")
		}
		failed[i] = true
	}
	indexes := make([]int, 0, len(replyErrors))
	for i, bad := range failed {
		if bad {
			indexes = append(indexes, i)
		}
	}
	return indexes, nil
}

func searchRequest(collection string, branch searchBranch, where filter, order searchOrder, limit int, fields []string) (*pb.SearchRequest, error) {
	request := &pb.SearchRequest{
		Collection: collection, Limit: uint32(limit), Filters: where.proto(), Uses_127Api: true,
		Properties: &pb.PropertiesRequest{NonRefProperties: fields},
		Metadata:   &pb.MetadataRequest{},
	}
	if order.field != "" {
		request.SortBy = []*pb.SortBy{{Path: []string{order.field}, Ascending: !order.desc}}
	}
	if branch.mode == "lexical" {
		request.Metadata.Score = true
		request.Bm25Search = &pb.BM25{Query: branch.query, Properties: branch.properties}
	}
	if branch.mode == "semantic" || branch.mode == "hybrid" {
		vector, err := vectorMessage("", branch.vector)
		if err != nil {
			return nil, err
		}
		vectors := []*pb.Vectors{vector}
		targets := &pb.Targets{TargetVectors: []string{branch.target}}
		if branch.mode == "semantic" {
			request.Metadata.Distance = true
			request.NearVector = &pb.NearVector{Vectors: vectors, Targets: targets}
		} else {
			fusion := pb.Hybrid_FUSION_TYPE_RELATIVE_SCORE
			if branch.ranked {
				fusion = pb.Hybrid_FUSION_TYPE_RANKED
			}
			alpha := float32(branch.alpha)
			request.Metadata.Score = true
			request.HybridSearch = &pb.Hybrid{
				Query: branch.query, Properties: branch.properties, Vectors: vectors, Targets: targets,
				FusionType: fusion, AlphaParam: &alpha, UseAlphaParam: true,
			}
		}
	}
	return request, nil
}

func logRecoveredGRPCPanic(ctx context.Context, path string, recovered any) {
	slog.ErrorContext(ctx, "recovered weaviate gRPC panic",
		"component", "weaviate.grpc",
		"path", path,
		"panic_type", fmt.Sprintf("%T", recovered),
		"stack", string(debug.Stack()),
	)
}

func classifyGRPCError(ctx, rpcCtx context.Context, err error) (classified error, tripBreaker bool) {
	code := status.Code(err)
	if code == codes.ResourceExhausted && strings.Contains(status.Convert(err).Message(), "received message larger than max") {
		return errResponseLimit, false
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("projection query failed (gRPC %s): %w", code, context.Canceled), false
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("projection query failed (gRPC %s): %w", code, context.DeadlineExceeded), false
	}
	if errors.Is(rpcCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || code == codes.DeadlineExceeded {
		return fmt.Errorf("projection query failed (gRPC %s): %w", code, context.DeadlineExceeded), true
	}
	if errors.Is(rpcCtx.Err(), context.Canceled) {
		return fmt.Errorf("projection query failed (gRPC %s): %w", code, context.Canceled), false
	}
	switch code {
	case codes.Unavailable, codes.Internal, codes.Unknown, codes.DataLoss, codes.ResourceExhausted:
		return fmt.Errorf("projection query failed (gRPC %s): %w", code, retrieval.ErrUnavailable), true
	default:
		return fmt.Errorf("projection query failed (gRPC %s)", code), false
	}
}

func (s *Store) grpcSearch(ctx context.Context, request *pb.SearchRequest, operation string, responseLimit int, callerDeadline bool) (results []*pb.SearchResult, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("projection query aborted: %w", ctxErr)
	}
	breaker := s.grpcBreaker()
	var isProbe bool
	if breaker != nil {
		var allowed bool
		allowed, isProbe = breaker.Allow()
		if !allowed {
			return nil, ErrCircuitOpen
		}
	}
	recorded := false
	defer func() {
		if r := recover(); r != nil {
			logRecoveredGRPCPanic(ctx, "search", r)
			if breaker != nil && !recorded {
				breaker.RecordFailure(isProbe)
				recorded = true
			}
			results = nil
			err = fmt.Errorf("projection query panic: %w", retrieval.ErrUnavailable)
			return
		}
		if breaker != nil && isProbe && !recorded {
			breaker.ReleaseProbe(true)
		}
	}()

	request.Tenant = tenantFrom(ctx)
	rpcCtx, cancel := s.rpcContext(ctx, operation, callerDeadline)
	defer cancel()
	if isProbe {
		if pooled := s.grpcPooled(); pooled != nil {
			pooled.pool.prepareProbe(rpcCtx)
		}
	}
	reply, callErr := s.grpc.Search(rpcCtx, request, grpc.MaxCallRecvMsgSize(responseLimit))
	if callErr != nil {
		classified, tripBreaker := classifyGRPCError(ctx, rpcCtx, callErr)
		if breaker != nil {
			if tripBreaker {
				breaker.RecordFailure(isProbe)
				recorded = true
			} else if isProbe {
				breaker.ReleaseProbe(true)
				recorded = true
			}
		}
		return nil, classified
	}
	if breaker != nil {
		breaker.RecordSuccess(isProbe)
		recorded = true
	}
	return reply.GetResults(), nil
}

func (s *Store) grpcSearchRows(ctx context.Context, collection string, branch searchBranch, where filter, order searchOrder, limit int, fields []string, responseLimit int, callerDeadline bool) (rows []itemRow, err error) {
	request, err := searchRequest(collection, branch, where, order, limit, fields)
	if err != nil {
		return nil, err
	}
	operation := telemetry.OpQuery
	if branch.mode == "" {
		operation = telemetry.OpRead
	}
	results, err := s.grpcSearch(ctx, request, operation, responseLimit, callerDeadline)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			logRecoveredGRPCPanic(ctx, "search_rows", r)
			rows = nil
			err = fmt.Errorf("projection decode panic: %w", retrieval.ErrUnavailable)
		}
	}()
	if hook := grpcDecodeRowHook.Load(); hook != nil {
		fn := *hook
		for _, result := range results {
			fn(result)
		}
	}
	rows = make([]itemRow, len(results))
	var scores []string
	var distances []float64
	for i, result := range results {
		fields := result.GetProperties().GetNonRefProps().GetFields()
		row := &rows[i]
		row.SegmentID = fields["segmentId"].GetTextValue()
		row.GenerationID = fields["generationId"].GetTextValue()
		row.VersionID = fields["versionId"].GetTextValue()
		row.Text = fields[passageText].GetTextValue()
		if m := result.GetMetadata(); m != nil {
			if m.ScorePresent {
				if scores == nil {
					scores = make([]string, len(results))
				}
				scores[i] = strconv.FormatFloat(float64(m.Score), 'g', -1, 32)
				row.Additional.Score = &scores[i]
			}
			if m.DistancePresent {
				if distances == nil {
					distances = make([]float64, len(results))
				}
				distances[i] = float64(m.Distance)
				row.Additional.Distance = &distances[i]
			}
		}
	}
	return rows, nil
}

func (s *Store) grpcProjectionRows(ctx context.Context, collection string, where filter, limit int, order searchOrder, properties []string, created bool, responseLimit int) (rows []projectionRow, err error) {
	request, err := searchRequest(collection, searchBranch{}, where, order, limit, properties)
	if err != nil {
		return nil, err
	}
	request.Metadata.Uuid = true
	request.Metadata.CreationTimeUnix = created
	results, err := s.grpcSearch(ctx, request, telemetry.OpRead, responseLimit, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			logRecoveredGRPCPanic(ctx, "projection_rows", r)
			rows = nil
			err = fmt.Errorf("projection decode panic: %w", retrieval.ErrUnavailable)
		}
	}()
	rows = make([]projectionRow, 0, len(results))
	for _, result := range results {
		fields := result.GetProperties().GetNonRefProps().GetFields()
		row := projectionRow{
			SegmentID: fields["segmentId"].GetTextValue(), Revision: fields[embeddingRevision].GetIntValue(),
			Kind: fields[itemKind].GetTextValue(), LexicalText: fields[lexicalProperty].GetTextValue(),
		}
		m := result.GetMetadata()
		row.Additional.ID = m.GetId()
		if id := m.GetIdAsBytes(); len(id) == 16 {
			row.Additional.ID = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
		}
		if row.Additional.ID == "" {
			return nil, errors.New("projection identity missing")
		}
		row.Additional.Created = m.GetCreationTimeUnix()
		rows = append(rows, row)
	}
	return rows, nil
}

const grpcAnchorBatchLimit = 16 << 20

func (s *Store) grpcAnchorProperties(ctx context.Context, collection string, ids []string) (out map[string]map[string]any, err error) {
	request := &pb.SearchRequest{
		Collection:  collection,
		Limit:       uint32(len(ids)),
		Filters:     containsAny("id", ids).proto(),
		Uses_127Api: true,
		Properties:  &pb.PropertiesRequest{ReturnAllNonrefProperties: true},
		Metadata:    &pb.MetadataRequest{Uuid: true},
	}
	results, err := s.grpcSearch(ctx, request, telemetry.OpRead, grpcAnchorBatchLimit, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			logRecoveredGRPCPanic(ctx, "anchor_properties", r)
			out = nil
			err = fmt.Errorf("projection decode panic: %w", retrieval.ErrUnavailable)
		}
	}()
	out = make(map[string]map[string]any, len(results))
	for _, result := range results {
		m := result.GetMetadata()
		id := m.GetId()
		if raw := m.GetIdAsBytes(); len(raw) == 16 {
			id = fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
		}
		if id == "" {
			return nil, errors.New("projection identity missing")
		}
		out[id] = protoProperties(result.GetProperties().GetNonRefProps())
	}
	return out, nil
}

func protoProperties(props *pb.Properties) map[string]any {
	fields := props.GetFields()
	out := make(map[string]any, len(fields))
	for key, val := range fields {
		if converted, ok := protoValue(val); ok {
			out[key] = converted
		}
	}
	return out
}

func protoValue(v *pb.Value) (any, bool) {
	if v == nil {
		return nil, false
	}
	switch kind := v.GetKind().(type) {
	case *pb.Value_TextValue:
		return kind.TextValue, true
	case *pb.Value_NumberValue:
		return kind.NumberValue, true
	case *pb.Value_IntValue:
		return kind.IntValue, true
	case *pb.Value_BoolValue:
		return kind.BoolValue, true
	case *pb.Value_DateValue:
		return kind.DateValue, true
	case *pb.Value_UuidValue:
		return kind.UuidValue, true
	case *pb.Value_BlobValue:
		return kind.BlobValue, true
	case *pb.Value_ObjectValue:
		return protoProperties(kind.ObjectValue), true
	case *pb.Value_ListValue:
		return protoListValue(kind.ListValue)
	case *pb.Value_NullValue:
		return nil, false
	default:
		return nil, false
	}
}

func protoListValue(list *pb.ListValue) (any, bool) {
	if list == nil {
		return nil, false
	}
	switch kind := list.GetKind().(type) {
	case *pb.ListValue_TextValues:
		return append([]string(nil), kind.TextValues.GetValues()...), true
	case *pb.ListValue_DateValues:
		return append([]string(nil), kind.DateValues.GetValues()...), true
	case *pb.ListValue_UuidValues:
		return append([]string(nil), kind.UuidValues.GetValues()...), true
	case *pb.ListValue_NumberValues:
		vals := kind.NumberValues.GetValues()
		out := make([]any, len(vals))
		for i, n := range vals {
			out[i] = n
		}
		return out, true
	case *pb.ListValue_IntValues:
		vals := kind.IntValues.GetValues()
		out := make([]any, len(vals))
		for i, n := range vals {
			out[i] = n
		}
		return out, true
	case *pb.ListValue_BoolValues:
		vals := kind.BoolValues.GetValues()
		out := make([]any, len(vals))
		for i, b := range vals {
			out[i] = b
		}
		return out, true
	case *pb.ListValue_ObjectValues:
		vals := kind.ObjectValues.GetValues()
		out := make([]any, len(vals))
		for i, o := range vals {
			out[i] = protoProperties(o)
		}
		return out, true
	default:
		return nil, false
	}
}
