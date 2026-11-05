package integrations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
)

// s3CallTimeout bounds every call the API itself makes to the store.
const s3CallTimeout = 10 * time.Second

// S3Storage is an ObjectStorage backed by any S3-compatible service: MinIO in
// local development, AWS S3 in production. Only configuration differs.
//
// It holds two clients that differ only in endpoint. The API talks to the store
// through client (inside Docker: http://minio:9000), while presigned URLs are
// signed against the public endpoint (http://localhost:9000). The host is part
// of an S3 signature, so signing with the Docker-internal hostname would
// produce URLs that browsers cannot reach.
type S3Storage struct {
	bucket    string
	client    *s3.Client
	presigner *s3.PresignClient
}

// NewS3Storage builds the S3 adapter. With no static credentials configured it
// uses the AWS default credential chain (environment, shared config, IAM role).
func NewS3Storage(ctx context.Context, cfg config.S3Config) (*S3Storage, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")))
	}
	base, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	publicEndpoint := cfg.PublicEndpoint
	if publicEndpoint == "" {
		publicEndpoint = cfg.Endpoint
	}
	return &S3Storage{
		bucket:    cfg.Bucket,
		client:    newS3Client(base, cfg.Endpoint, cfg.UsePathStyle),
		presigner: s3.NewPresignClient(newS3Client(base, publicEndpoint, cfg.UsePathStyle)),
	}, nil
}

func newS3Client(base aws.Config, endpoint string, pathStyle bool) *s3.Client {
	return s3.NewFromConfig(base, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = pathStyle
		// Recent SDK versions add a CRC32 checksum to PutObject by default. In a
		// presigned URL that would require the browser to send a matching
		// checksum header, so only compute checksums when the API requires them.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
}

// PresignUpload signs a PUT for exactly one key, content type and size. The
// content type and length are part of the signature, so the store rejects any
// request that deviates from what the API validated.
func (s *S3Storage) PresignUpload(ctx context.Context, req PresignRequest) (PresignedUpload, error) {
	expiresAt := time.Now().Add(req.TTL)
	signed, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(req.ObjectKey),
		ContentType:   aws.String(req.ContentType),
		ContentLength: aws.Int64(req.SizeBytes),
	}, s3.WithPresignExpires(req.TTL))
	if err != nil {
		return PresignedUpload{}, fmt.Errorf("presign put object: %w", err)
	}

	// Headers the browser must send. Host and Content-Length are set by the
	// browser itself (and cannot be overridden from JavaScript).
	headers := map[string]string{}
	for name, values := range signed.SignedHeader {
		if len(values) == 0 || strings.EqualFold(name, "host") || strings.EqualFold(name, "content-length") {
			continue
		}
		headers[name] = values[0]
	}
	return PresignedUpload{
		Method:    signed.Method,
		URL:       signed.URL,
		Headers:   headers,
		ObjectKey: req.ObjectKey,
		ExpiresAt: expiresAt,
	}, nil
}

// StatObject reports the object's real size and content type via HeadObject.
func (s *S3Storage) StatObject(ctx context.Context, objectKey string) (ObjectInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, s3CallTimeout)
	defer cancel()

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isNotFound(err) {
			return ObjectInfo{}, ErrObjectNotFound
		}
		return ObjectInfo{}, fmt.Errorf("head object: %w", err)
	}
	return ObjectInfo{
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
		ETag:        strings.Trim(aws.ToString(out.ETag), `"`),
	}, nil
}

// DeleteObject removes the object. S3 reports success for keys that do not exist.
func (s *S3Storage) DeleteObject(ctx context.Context, objectKey string) error {
	ctx, cancel := context.WithTimeout(ctx, s3CallTimeout)
	defer cancel()

	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	}); err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

// GetObject opens an object for streaming. The caller closes the body; the
// caller's context (not the short API-call timeout) bounds the transfer.
func (s *S3Storage) GetObject(ctx context.Context, objectKey string) (ObjectReader, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isNotFound(err) {
			return ObjectReader{}, ErrObjectNotFound
		}
		return ObjectReader{}, fmt.Errorf("get object: %w", err)
	}
	return ObjectReader{Body: out.Body, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType)}, nil
}

// PutObject uploads size bytes. The body must be seekable so the SDK can sign the payload.
func (s *S3Storage) PutObject(ctx context.Context, objectKey, contentType string, body io.ReadSeeker, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey),
		Body:          body,
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
	})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

// DeletePrefix deletes every object under prefix, one request per key (outputs are
// small in number, and this avoids the checksum requirements of batch deletes).
func (s *S3Storage) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if prefix == "" {
		return 0, errors.New("refusing to delete an empty prefix")
	}
	deleted := 0
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return deleted, fmt.Errorf("list objects: %w", err)
		}
		for _, obj := range page.Contents {
			if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(s.bucket),
				Key:    obj.Key,
			}); err != nil {
				return deleted, fmt.Errorf("delete object: %w", err)
			}
			deleted++
		}
	}
	return deleted, nil
}

// isNotFound recognises the "no such key" answers of S3 and S3-compatible stores.
// HeadObject has no response body, so the SDK reports a bare NotFound.
func isNotFound(err error) bool {
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey":
			return true
		}
	}
	return false
}
