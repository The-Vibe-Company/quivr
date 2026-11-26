package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type objectMeta struct {
	Key          string
	ETag         string
	Size         int64
	LastModified time.Time
}

type storage struct {
	transport *http.Transport
	client    *s3.Client
	cfg       prefixConfig
}

func newStorage(c prefixConfig, credential quivrplugin.Credential) (*storage, error) {
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
	client := s3.NewFromConfig(aws.Config{
		Region:      c.Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(secret.AccessKeyID, secret.SecretAccessKey, secret.SessionToken)),
		HTTPClient: &http.Client{
			Transport:     tr,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, func(o *s3.Options) {
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
			return quivrplugin.SourceError("object_changed", "the object changed after it was listed")
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return quivrplugin.SourceError("object_missing", "the object or bucket is unavailable")
		}
	}
	return quivrplugin.TransientError("storage_unavailable", "object storage did not complete the request")
}

// listAfterKey streams matching objects lexicographically after startAfter.
// It returns up to limit objects plus a boolean indicating whether more objects
// remain under the prefix after the returned slice.
func (s *storage) listAfterKey(ctx context.Context, startAfter string, limit int) ([]objectMeta, bool, error) {
	input := &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.cfg.Bucket),
		Prefix:  aws.String(s.cfg.Prefix),
		MaxKeys: aws.Int32(1000),
	}
	if startAfter != "" {
		input.StartAfter = aws.String(startAfter)
	}
	pages := s3.NewListObjectsV2Paginator(s.client, input)
	out := make([]objectMeta, 0, min(limit, 128))
	for pages.HasMorePages() {
		p, err := pages.NextPage(ctx)
		if err != nil {
			return nil, false, storageError(err)
		}
		for _, entry := range p.Contents {
			key := aws.ToString(entry.Key)
			if startAfter != "" && key <= startAfter {
				continue
			}
			if !s.cfg.acceptsKey(key) {
				continue
			}
			etag := aws.ToString(entry.ETag)
			if len(key) > 512 || len(etag) > 256 || etag == "" {
				return nil, false, quivrplugin.SourceError("unsupported_object_identity", "object key or ETag exceeds the reference bound")
			}
			if len(out) == limit {
				return out, true, nil
			}
			out = append(out, objectMeta{
				Key:          key,
				ETag:         etag,
				Size:         aws.ToInt64(entry.Size),
				LastModified: aws.ToTime(entry.LastModified).UTC(),
			})
		}
	}
	return out, false, nil
}

// listAfterTimestamp scans the prefix and returns all matching objects strictly
// after (afterTime, afterKey) sorted deterministically by (LastModified ASC, Key ASC).
func (s *storage) listAfterTimestamp(ctx context.Context, afterTime time.Time, afterKey string) ([]objectMeta, error) {
	input := &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.cfg.Bucket),
		Prefix:  aws.String(s.cfg.Prefix),
		MaxKeys: aws.Int32(1000),
	}
	pages := s3.NewListObjectsV2Paginator(s.client, input)
	var out []objectMeta
	for pages.HasMorePages() {
		p, err := pages.NextPage(ctx)
		if err != nil {
			return nil, storageError(err)
		}
		for _, entry := range p.Contents {
			key := aws.ToString(entry.Key)
			if !s.cfg.acceptsKey(key) {
				continue
			}
			etag := aws.ToString(entry.ETag)
			if len(key) > 512 || len(etag) > 256 || etag == "" {
				return nil, quivrplugin.SourceError("unsupported_object_identity", "object key or ETag exceeds the reference bound")
			}
			mtime := aws.ToTime(entry.LastModified).UTC()
			if !afterTime.IsZero() {
				if mtime.Before(afterTime) || (mtime.Equal(afterTime) && key <= afterKey) {
					continue
				}
			}
			out = append(out, objectMeta{
				Key:          key,
				ETag:         etag,
				Size:         aws.ToInt64(entry.Size),
				LastModified: mtime,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastModified.Equal(out[j].LastModified) {
			return out[i].LastModified.Before(out[j].LastModified)
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func (s *storage) get(ctx context.Context, key, etag string, maxBytes int64) ([]byte, string, error) {
	in := &s3.GetObjectInput{
		Bucket:  aws.String(s.cfg.Bucket),
		Key:     aws.String(key),
		IfMatch: aws.String(etag),
	}
	p, err := s.client.GetObject(ctx, in)
	if err != nil {
		return nil, "", storageError(err)
	}
	defer p.Body.Close()
	if gotETag := aws.ToString(p.ETag); gotETag != "" && gotETag != etag {
		return nil, "", quivrplugin.SourceError("object_changed", "object identity changed during download")
	}
	data, err := io.ReadAll(io.LimitReader(p.Body, maxBytes+1))
	if err != nil {
		return nil, "", quivrplugin.TransientError("storage_unavailable", "object download did not complete")
	}
	if int64(len(data)) > maxBytes {
		return nil, "", quivrplugin.SourceError("object_too_large", "object exceeds max_object_bytes")
	}
	return data, aws.ToString(p.ContentType), nil
}

func (s *storage) close() { s.transport.CloseIdleConnections() }
