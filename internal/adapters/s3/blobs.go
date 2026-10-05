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
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
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
	bucket string
}

func New(cfg Config) *Store { return newWithTransport(cfg, outbound.Transport(nil)) }

// Canonical hydration fans out across blobs. Retain connections between batches;
// the default two idle connections churn ephemeral ports under concurrent searches.
const canonicalIdleConnections = 128

func newWithTransport(cfg Config, transport *http.Transport) *Store {
	transport.MaxIdleConnsPerHost = canonicalIdleConnections
	transport.MaxIdleConns = canonicalIdleConnections
	client := awss3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")), HTTPClient: &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: outbound.CheckRedirect}}, func(o *awss3.Options) { o.BaseEndpoint = aws.String(cfg.Endpoint); o.UsePathStyle = true })
	return &Store{client: client, bucket: cfg.Bucket}
}
func (s *Store) Bootstrap(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err == nil {
		return nil
	}
	if _, err := s.client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return errors.New("S3 bucket bootstrap unavailable")
	}
	return nil
}
func (s *Store) Ready(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return err
}
func (s *Store) Put(ctx context.Context, org string, data []byte) (content.Blob, error) {
	digest := content.Hash(data)
	b := content.Blob{Key: content.Hash([]byte(org)) + "/sha256/" + digest, SHA256: digest, Size: int64(len(data))}
	checksum, _ := hex.DecodeString(digest)
	_, _ = s.client.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(b.Key), Body: bytes.NewReader(data), ContentLength: aws.Int64(b.Size), ContentType: aws.String("application/octet-stream"), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(checksum)), IfNoneMatch: aws.String("*"), Metadata: map[string]string{"sha256": digest}})
	// Both a successful PUT and an ambiguous/lost response must prove the immutable bytes.
	if _, err := s.Read(ctx, b); err != nil {
		return content.Blob{}, errors.New("immutable S3 object could not be verified")
	}
	return b, nil
}
func (s *Store) Read(ctx context.Context, b content.Blob) ([]byte, error) {
	if b.Size < 0 || b.Size > 2<<20 {
		return nil, errors.New("unsupported canonical object size")
	}
	object, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(b.Key)})
	var missing *types.NoSuchKey
	if errors.As(err, &missing) {
		return nil, fmt.Errorf("canonical S3 object absent: %w", content.ErrArtifactMissing)
	}
	if err != nil {
		return nil, errors.New("canonical S3 object unavailable")
	}
	defer object.Body.Close()
	data, err := io.ReadAll(io.LimitReader(object.Body, b.Size+1))
	if err != nil {
		return nil, errors.New("canonical S3 read failed")
	}
	if int64(len(data)) != b.Size || content.Hash(data) != b.SHA256 {
		return nil, fmt.Errorf("canonical S3 checksum mismatch: %w", content.ErrArtifactCorrupt)
	}
	return data, nil
}
