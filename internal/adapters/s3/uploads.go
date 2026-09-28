package s3

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// PresignPut issues a single presigned PUT capability and the exact headers the
// client must send. The checksum and media type are part of the signature.
func (s *Store) PresignPut(ctx context.Context, objectKey string, size int64, sha256hex, mediaType string, ttl time.Duration) (string, map[string]string, error) {
	if size < 1 || size > uploads.MaxUploadBytes || mediaType == "" {
		return "", nil, errors.New("invalid transfer expectation")
	}
	checksum, err := hex.DecodeString(sha256hex)
	if err != nil || len(checksum) != 32 {
		return "", nil, errors.New("invalid transfer checksum")
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	presigner := awss3.NewPresignClient(s.client)
	request, err := presigner.PresignPutObject(ctx, &awss3.PutObjectInput{
		Bucket:         aws.String(s.bucket),
		Key:            aws.String(objectKey),
		ContentLength:  aws.Int64(size),
		ContentType:    aws.String(mediaType),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(checksum)),
	}, awss3.WithPresignExpires(ttl))
	if err != nil {
		return "", nil, errors.New("presigned transfer unavailable")
	}
	headers := map[string]string{}
	for name, values := range request.SignedHeader {
		if strings.EqualFold(name, "host") || len(values) == 0 {
			continue
		}
		headers[name] = values[0]
	}
	return request.URL, headers, nil
}

// Verify proves read-after-write: the stored bytes match the expected length and
// digest. A mismatch is terminal; an unavailable object is retryable.
func (s *Store) Verify(ctx context.Context, objectKey string, size int64, sha256hex string) error {
	if size < 1 || size > uploads.MaxUploadBytes {
		return errors.New("unsupported transfer size")
	}
	object, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(objectKey)})
	if err != nil {
		return errors.New("transferred object unavailable")
	}
	defer object.Body.Close()
	// Hash while streaming so verification memory does not grow with the object.
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(object.Body, size+1))
	if err != nil {
		return errors.New("transferred object read failed")
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != sha256hex {
		return fmt.Errorf("transferred bytes do not match expectation: %w", uploads.ErrVerificationMismatch)
	}
	return nil
}

// PutStream writes server-collected bytes under a content-addressed key. The
// declared checksum is enforced by storage and the caller verifies the stored
// bytes afterwards. A present object of the expected length is not rewritten.
// Storage may refuse the conditional write before reading the body and close
// the connection, and a stored write can lose its response, so any failed
// write is resolved by observing the object rather than by its error shape.
func (s *Store) PutStream(ctx context.Context, objectKey string, body io.ReadSeeker, size int64, sha256hex, mediaType string) error {
	checksum, err := hex.DecodeString(sha256hex)
	if err != nil || len(checksum) != 32 || size < 1 || size > uploads.MaxUploadBytes || mediaType == "" {
		return errors.New("invalid deposit expectation")
	}
	if s.holds(ctx, objectKey, size) {
		return nil
	}
	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(objectKey), Body: body, ContentLength: aws.Int64(size), ContentType: aws.String(mediaType), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(checksum)), IfNoneMatch: aws.String("*")})
	if err != nil && !s.holds(ctx, objectKey, size) {
		return errors.New("deposit storage unavailable")
	}
	return nil
}

// holds reports whether an object of the expected length is stored at the key.
func (s *Store) holds(ctx context.Context, objectKey string, size int64) bool {
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(objectKey)})
	return err == nil && head.ContentLength != nil && *head.ContentLength == size
}

// ReadRange reads an inclusive byte range of a stored object.
func (s *Store) ReadRange(ctx context.Context, objectKey string, start, end int64) ([]byte, error) {
	if start < 0 || end < start {
		return nil, errors.New("invalid range")
	}
	object, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(objectKey), Range: aws.String(fmt.Sprintf("bytes=%d-%d", start, end))})
	if err != nil {
		return nil, errors.New("canonical S3 range unavailable")
	}
	defer object.Body.Close()
	data, err := io.ReadAll(io.LimitReader(object.Body, end-start+2))
	if err != nil {
		return nil, errors.New("canonical S3 range read failed")
	}
	return data, nil
}
