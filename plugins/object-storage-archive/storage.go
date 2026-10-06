package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type object struct {
	Key  string
	ETag string
	Size int64
}
type storage struct {
	transport *http.Transport
	client    *s3.Client
	cfg       archiveConfig
}

func newStorage(c archiveConfig, credential quivrplugin.Credential) (*storage, error) {
	var secret struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		SessionToken    string `json:"session_token"`
	}
	if credential.Decode(&secret) != nil || secret.AccessKeyID == "" || secret.SecretAccessKey == "" {
		return nil, quivrplugin.AccessError("missing_credential", "deposit object-storage credentials")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 30 * time.Second
	tr.MaxIdleConnsPerHost = 32
	client := s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(secret.AccessKeyID, secret.SecretAccessKey, secret.SessionToken)), HTTPClient: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
			o.UsePathStyle = true
		}
	})
	return &storage{client: client, cfg: c, transport: tr}, nil
}

// Never expose SDK errors: they may include endpoints, signed requests or keys.
func storageError(err error) error {
	if err == nil {
		return nil
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidToken":
			return quivrplugin.AccessError("storage_access_denied", "object storage refused the credential")
		case "PreconditionFailed":
			return quivrplugin.SourceError("archive_changed", "the archive changed after its checkpoint was recorded")
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return quivrplugin.SourceError("archive_missing", "the archive or bucket is unavailable")
		}
	}
	return quivrplugin.TransientError("storage_unavailable", "object storage did not complete the request")
}
func (s *storage) next(ctx context.Context, after string) (object, bool, error) {
	input := &s3.ListObjectsV2Input{Bucket: aws.String(s.cfg.Bucket), Prefix: aws.String(s.cfg.Prefix), MaxKeys: aws.Int32(1000)}
	if after != "" {
		input.StartAfter = aws.String(after)
	}
	pages := s3.NewListObjectsV2Paginator(s.client, input)
	for pages.HasMorePages() {
		p, err := pages.NextPage(ctx)
		if err != nil {
			return object{}, false, storageError(err)
		}
		for _, entry := range p.Contents {
			key := aws.ToString(entry.Key)
			if s.cfg.acceptsArchive(key) {
				if len(key) > 512 || len(aws.ToString(entry.ETag)) > 256 || aws.ToString(entry.ETag) == "" {
					return object{}, false, quivrplugin.SourceError("unsupported_archive_identity", "archive key or ETag exceeds the reference bound")
				}
				return object{key, aws.ToString(entry.ETag), aws.ToInt64(entry.Size)}, true, nil
			}
		}
	}
	return object{}, false, nil
}
func (s *storage) head(ctx context.Context, key, etag string) (object, error) {
	in := &s3.HeadObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)}
	if etag != "" {
		in.IfMatch = aws.String(etag)
	}
	p, err := s.client.HeadObject(ctx, in)
	if err != nil {
		return object{}, storageError(err)
	}
	if etag != "" && aws.ToString(p.ETag) != etag {
		return object{}, quivrplugin.SourceError("archive_changed", "archive identity no longer matches the checkpoint")
	}
	return object{key, aws.ToString(p.ETag), aws.ToInt64(p.ContentLength)}, nil
}
func (s *storage) open(ctx context.Context, obj object, byteRange string) (io.ReadCloser, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(obj.Key), IfMatch: aws.String(obj.ETag)}
	if byteRange != "" {
		in.Range = aws.String(byteRange)
	}
	p, err := s.client.GetObject(ctx, in)
	if err != nil {
		return nil, storageError(err)
	}
	if aws.ToString(p.ETag) != obj.ETag {
		p.Body.Close()
		return nil, quivrplugin.SourceError("archive_changed", "archive identity no longer matches the checkpoint")
	}
	if byteRange != "" && aws.ToString(p.ContentRange) != fmt.Sprintf("bytes %s/%d", strings.TrimPrefix(byteRange, "bytes="), obj.Size) {
		p.Body.Close()
		return nil, quivrplugin.SourceError("archive_range_unsupported", "storage did not return the requested archive range")
	}
	return p.Body, nil
}

func (s *storage) close() { s.transport.CloseIdleConnections() }
