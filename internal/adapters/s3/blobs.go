package s3

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type Config struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Bucket    string `json:"bucket"`
}
type Store struct {
	client *awss3.Client
	// blobClient has no client-wide timeout: each blob transfer carries a
	// deadline sized by transferTimeout.
	blobClient *awss3.Client
	bucket     string
	metrics    atomic.Pointer[telemetry.DependencyMetrics]
}

// SetMetrics attaches process-local dependency call counters to this store.
func (s *Store) SetMetrics(m *telemetry.DependencyMetrics) {
	if s != nil {
		s.metrics.Store(m)
	}
}

func (s *Store) recordCall(op string) {
	if m := s.metrics.Load(); m != nil {
		m.Record(telemetry.DependencyS3, telemetry.ProtocolHTTP, op)
	}
}

func New(cfg Config) *Store { return newWithTransport(cfg, outbound.Transport(nil)) }

// Canonical hydration fans out across blobs. Retain connections between batches;
// the default two idle connections churn ephemeral ports under concurrent searches.
const canonicalIdleConnections = 128

func newWithTransport(cfg Config, transport *http.Transport) *Store {
	transport.MaxIdleConnsPerHost = canonicalIdleConnections
	transport.MaxIdleConns = canonicalIdleConnections
	client := awss3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")), HTTPClient: &http.Client{Timeout: requestTimeout, Transport: telemetry.Transport(transport, "s3.request"), CheckRedirect: outbound.CheckRedirect}}, func(o *awss3.Options) { o.BaseEndpoint = aws.String(cfg.Endpoint); o.UsePathStyle = true })
	blobClient := awss3.New(client.Options(), func(o *awss3.Options) {
		httpClient := *o.HTTPClient.(*http.Client)
		httpClient.Timeout = 0
		o.HTTPClient = &httpClient
	})
	return &Store{client: client, blobClient: blobClient, bucket: cfg.Bucket}
}

// A fixed bound times out large blobs on a slow link; no bound lets a stalled
// connection hold a transfer until the caller gives up. Blob transfers get the
// control-request bound plus the time to move their bytes at a floor rate, and
// still end earlier when the caller's deadline does.
const (
	requestTimeout    = 5 * time.Second
	transferFloorRate = 512 << 10 // bytes per second
)

func transferTimeout(size int64) time.Duration {
	return requestTimeout + time.Duration(size)*time.Second/transferFloorRate
}
func (s *Store) Bootstrap(ctx context.Context) error {
	s.recordCall(telemetry.OpBucket)
	if _, err := s.client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err == nil {
		return nil
	}
	s.recordCall(telemetry.OpBucket)
	if _, err := s.client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return errors.New("S3 bucket bootstrap unavailable")
	}
	return nil
}
func (s *Store) Ready(ctx context.Context) error {
	s.recordCall(telemetry.OpBucket)
	_, err := s.client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return err
}
func (s *Store) Put(ctx context.Context, org string, data []byte) (content.Blob, error) {
	return s.put(ctx, org, data, 2<<20)
}

func (s *Store) PutVectorFile(ctx context.Context, org string, data []byte) (content.Blob, error) {
	return s.put(ctx, org, data, content.MaxVectorFileBytes)
}

func (s *Store) put(ctx context.Context, org string, data []byte, limit int64) (content.Blob, error) {
	if int64(len(data)) > limit {
		return content.Blob{}, content.ErrInvalid
	}
	digest := content.Hash(data)
	b := content.Blob{Key: content.Hash([]byte(org)) + "/sha256/" + digest, SHA256: digest, Size: int64(len(data))}
	sum, _ := hex.DecodeString(digest)
	checksum := base64.StdEncoding.EncodeToString(sum)
	s.recordCall(telemetry.OpPutObject)
	putCtx, cancel := context.WithTimeout(ctx, transferTimeout(b.Size))
	written, err := s.blobClient.PutObject(putCtx, &awss3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(b.Key), Body: bytes.NewReader(data), ContentLength: aws.Int64(b.Size), ContentType: aws.String("application/octet-stream"), ChecksumSHA256: aws.String(checksum), IfNoneMatch: aws.String("*"), Metadata: map[string]string{"sha256": digest}})
	cancel()
	// S3 refuses a body that does not match the supplied SHA-256 and echoes the
	// checksum it validated. A created object acknowledged with that checksum
	// holds exactly these bytes. Every other outcome — a lost or reset response,
	// a 5xx, 412 for an existing key, a store that ignores checksums — must
	// prove the immutable bytes by reading them back.
	if err == nil && acknowledgedChecksum(written, checksum) {
		return b, nil
	}
	if _, err = s.read(ctx, b, limit); err != nil {
		if errors.Is(err, content.ErrArtifactCorrupt) {
			return content.Blob{}, fmt.Errorf("immutable S3 object could not be verified: %w", err)
		}
		return content.Blob{}, errors.New("immutable S3 object could not be verified")
	}
	return b, nil
}

func acknowledgedChecksum(out *awss3.PutObjectOutput, checksum string) bool {
	// A composite checksum digests parts, not the complete byte stream.
	return out != nil && aws.ToString(out.ChecksumSHA256) == checksum && out.ChecksumType != types.ChecksumTypeComposite
}

func (s *Store) Read(ctx context.Context, b content.Blob) ([]byte, error) {
	return s.read(ctx, b, 2<<20)
}

func (s *Store) ReadVectorFile(ctx context.Context, b content.Blob) ([]byte, error) {
	return s.read(ctx, b, content.MaxVectorFileBytes)
}

func (s *Store) read(ctx context.Context, b content.Blob, limit int64) ([]byte, error) {
	if b.Size < 0 || b.Size > limit {
		return nil, errors.New("unsupported canonical object size")
	}
	ctx, cancel := context.WithTimeout(ctx, transferTimeout(b.Size))
	defer cancel()
	s.recordCall(telemetry.OpGetObject)
	object, err := s.blobClient.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(b.Key)})
	var missing *types.NoSuchKey
	if errors.As(err, &missing) {
		return nil, fmt.Errorf("canonical S3 object absent: %w", content.ErrArtifactMissing)
	}
	if err != nil {
		return nil, errors.New("canonical S3 object unavailable")
	}
	defer object.Body.Close()
	data, err := readBounded(object.Body, b.Size, aws.ToInt64(object.ContentLength) == b.Size)
	if errors.Is(err, errOversized) {
		return nil, fmt.Errorf("canonical S3 checksum mismatch: %w", content.ErrArtifactCorrupt)
	}
	if err != nil {
		return nil, errors.New("canonical S3 read failed")
	}
	if int64(len(data)) != b.Size || content.Hash(data) != b.SHA256 {
		return nil, fmt.Errorf("canonical S3 checksum mismatch: %w", content.ErrArtifactCorrupt)
	}
	return data, nil
}

// unknownLengthChunk is the first buffer for a body whose declared length does
// not match the expected size; it then doubles up to that size.
const unknownLengthChunk = 1 << 20

var errOversized = errors.New("object larger than its identity")

// readBounded reads at most size bytes and proves the body ends there. When the
// declared length matches, the bytes land in one exact buffer. Otherwise the
// buffer grows with the bytes received and never past size, so a short body
// cannot force a size-byte allocation. A short body returns its bytes without
// error; a body longer than size returns errOversized.
func readBounded(body io.Reader, size int64, declared bool) ([]byte, error) {
	capacity := size
	if !declared {
		capacity = min(size, unknownLengthChunk)
	}
	data := make([]byte, 0, capacity)
	for int64(len(data)) < size {
		if len(data) == cap(data) {
			grown := make([]byte, len(data), min(size, 2*int64(cap(data))))
			copy(grown, data)
			data = grown
		}
		n, err := body.Read(data[len(data):cap(data)])
		data = data[:len(data)+n]
		if err == io.EOF {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
	}
	var extra [1]byte
	for {
		n, err := body.Read(extra[:])
		if n > 0 {
			return nil, errOversized
		}
		if err == io.EOF {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
