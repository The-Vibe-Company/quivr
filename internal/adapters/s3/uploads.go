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

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
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

// Verify proves the stored length and digest. A store-validated full-object
// checksum avoids downloading the payload; legacy objects and stores without
// checksum HEAD support retain bounded streaming verification. A mismatch is
// terminal; an unavailable object is retryable.
func (s *Store) Verify(ctx context.Context, objectKey string, size int64, sha256hex string) error {
	if size < 1 || size > uploads.MaxUploadBytes {
		return errors.New("unsupported transfer size")
	}
	s.recordCall(telemetry.OpHeadObject)
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(objectKey), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		// Some compatible stores reject checksum mode or do not implement HEAD.
		// Other failures (missing objects, access denial, outages) stay retryable.
		var response *smithyhttp.ResponseError
		if !errors.As(err, &response) || (response.HTTPStatusCode() != 400 && response.HTTPStatusCode() != 405 && response.HTTPStatusCode() != 501) {
			return errors.New("transferred object unavailable")
		}
	} else {
		if head.ContentLength != nil && *head.ContentLength != size {
			return fmt.Errorf("transferred size does not match expectation: %w", uploads.ErrVerificationMismatch)
		}
		checksum := aws.ToString(head.ChecksumSHA256)
		// Multipart SHA-256 is composite, not a digest of the complete byte stream.
		if head.ContentLength != nil && checksum != "" && head.ChecksumType != types.ChecksumTypeComposite && !strings.Contains(checksum, "-") {
			digest, decodeErr := base64.StdEncoding.DecodeString(checksum)
			if decodeErr != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != sha256hex {
				return fmt.Errorf("transferred checksum does not match expectation: %w", uploads.ErrVerificationMismatch)
			}
			return nil
		}
	}
	s.recordCall(telemetry.OpGetObject)
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

// PresignGet issues a short-lived signed GET reference to one stored object,
// such as the input Blob of a plugin invocation. The URL reads only that
// object, and only until it expires.
func (s *Store) PresignGet(ctx context.Context, objectKey string, ttl time.Duration) (string, time.Time, error) {
	if objectKey == "" || ttl <= 0 {
		return "", time.Time{}, errors.New("invalid signed reference request")
	}
	expires := time.Now().Add(ttl)
	request, err := awss3.NewPresignClient(s.client).PresignGetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(objectKey)}, awss3.WithPresignExpires(ttl))
	if err != nil {
		return "", time.Time{}, errors.New("signed reference unavailable")
	}
	return request.URL, expires, nil
}
