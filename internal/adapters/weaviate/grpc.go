package weaviate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

func (s *Store) rpcContext(ctx context.Context, operation string, callerDeadline bool) (context.Context, context.CancelFunc) {
	if metrics := s.metrics.Load(); metrics != nil {
		metrics.Record(telemetry.DependencyWeaviate, telemetry.ProtocolGRPC, operation)
	}
	if s.apiKey != "" {
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = metadata.MD{}
		}
		md.Set("authorization", "Bearer "+s.apiKey)
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	if _, bounded := ctx.Deadline(); callerDeadline && bounded || s.Client.Timeout == 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.Client.Timeout)
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
	out := &pb.BatchObject{Collection: object.Class, Uuid: object.ID, Properties: &pb.BatchObject_Properties{}}
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
	for name, vector := range object.Vectors {
		packed, err := vectorMessage(name, vector)
		if err != nil {
			return nil, err
		}
		out.Vectors = append(out.Vectors, packed)
	}
	return out, nil
}

func (s *Store) grpcInsertBatch(ctx context.Context, objects []projectionObject) error {
	request := &pb.BatchObjectsRequest{Objects: make([]*pb.BatchObject, 0, len(objects))}
	for _, object := range objects {
		packed, err := batchObject(object)
		if err != nil {
			return errors.New("invalid projection batch object")
		}
		request.Objects = append(request.Objects, packed)
	}
	rpcCtx, cancel := s.rpcContext(ctx, telemetry.OpWrite, false)
	defer cancel()
	reply, err := s.grpc.BatchObjects(rpcCtx, request)
	if err != nil {
		switch status.Code(err) {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unknown, codes.Internal:
			return s.verifyBatch(ctx, objects)
		default:
			return fmt.Errorf("projection batch request failed (gRPC %s)", status.Code(err))
		}
	}
	if len(reply.GetErrors()) > 0 {
		return errors.New("projection publication failed")
	}
	return nil
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

func (s *Store) grpcSearch(ctx context.Context, request *pb.SearchRequest, operation string, responseLimit int, callerDeadline bool) ([]*pb.SearchResult, error) {
	rpcCtx, cancel := s.rpcContext(ctx, operation, callerDeadline)
	defer cancel()
	reply, err := s.grpc.Search(rpcCtx, request, grpc.MaxCallRecvMsgSize(responseLimit))
	if err != nil {
		if status.Code(err) == codes.ResourceExhausted && strings.Contains(status.Convert(err).Message(), "received message larger than max") {
			return nil, errResponseLimit
		}
		return nil, fmt.Errorf("projection query failed (gRPC %s)", status.Code(err))
	}
	return reply.GetResults(), nil
}

func (s *Store) grpcSearchRows(ctx context.Context, collection string, branch searchBranch, where filter, order searchOrder, limit int, fields []string, responseLimit int, callerDeadline bool) ([]itemRow, error) {
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
	rows := make([]itemRow, 0, len(results))
	for _, result := range results {
		fields := result.GetProperties().GetNonRefProps().GetFields()
		row := itemRow{
			SegmentID: fields["segmentId"].GetTextValue(), GenerationID: fields["generationId"].GetTextValue(),
			VersionID: fields["versionId"].GetTextValue(), Text: fields[passageText].GetTextValue(),
		}
		if m := result.GetMetadata(); m != nil {
			if m.ScorePresent {
				score := strconv.FormatFloat(float64(m.Score), 'g', -1, 32)
				row.Additional.Score = &score
			}
			if m.DistancePresent {
				distance := float64(m.Distance)
				row.Additional.Distance = &distance
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Store) grpcProjectionRows(ctx context.Context, collection string, where filter, limit int, order searchOrder, properties []string, created bool) ([]projectionRow, error) {
	request, err := searchRequest(collection, searchBranch{}, where, order, limit, properties)
	if err != nil {
		return nil, err
	}
	request.Metadata.Uuid = true
	request.Metadata.CreationTimeUnix = created
	results, err := s.grpcSearch(ctx, request, telemetry.OpRead, 2<<20, false)
	if err != nil {
		return nil, err
	}
	rows := make([]projectionRow, 0, len(results))
	for _, result := range results {
		fields := result.GetProperties().GetNonRefProps().GetFields()
		row := projectionRow{
			SegmentID: fields["segmentId"].GetTextValue(), Revision: fields[embeddingRevision].GetIntValue(),
			Kind: fields[itemKind].GetTextValue(),
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
