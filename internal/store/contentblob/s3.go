package contentblob

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

type s3Client interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// S3 stores immutable project-content version bodies in the platform bucket.
type S3 struct {
	client s3Client
	bucket string
}

// NewS3FromEnv builds the project-content blob store from the same S3 settings
// used by run artifacts and workspace checkpoints.
func NewS3FromEnv() (*S3, error) {
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if bucket == "" {
		return nil, fmt.Errorf("S3_BUCKET is required for project assets")
	}
	region := strings.TrimSpace(os.Getenv("S3_REGION"))
	if region == "" {
		region = "us-east-1"
	}
	cfg := aws.Config{Region: region}
	accessKeyID := strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID"))
	secretAccessKey := strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY"))
	if (accessKeyID == "") != (secretAccessKey == "") {
		return nil, fmt.Errorf("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set together for project assets")
	}
	if accessKeyID != "" {
		cfg.Credentials = credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")
	}

	endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	if endpoint != "" {
		parsed, err := url.ParseRequestURI(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("S3_ENDPOINT must be an absolute URL")
		}
	}
	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
			options.UsePathStyle = true
		}
	})
	return &S3{client: client, bucket: bucket}, nil
}

func (s *S3) Put(ctx context.Context, key string, content []byte, mediaType string) error {
	checksum := crc32.ChecksumIEEE(content)
	input := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(content),
		ContentLength: aws.Int64(int64(len(content))),
		ChecksumCRC32: aws.String(base64.StdEncoding.EncodeToString([]byte{byte(checksum >> 24), byte(checksum >> 16), byte(checksum >> 8), byte(checksum)})),
	}
	if mediaType = strings.TrimSpace(mediaType); mediaType != "" {
		input.ContentType = aws.String(mediaType)
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("putting project asset object %q in S3 bucket %q: %w", key, s.bucket, err)
	}
	return nil
}

func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, store.ErrContentBlobNotFound
		}
		return nil, fmt.Errorf("getting project asset object %q from S3 bucket %q: %w", key, s.bucket, err)
	}
	if output.ContentLength != nil && *output.ContentLength > store.MaxProjectContentVersionBytes {
		_ = output.Body.Close()
		return nil, fmt.Errorf("project asset object %q exceeds the %d-byte limit", key, store.MaxProjectContentVersionBytes)
	}
	body, readErr := io.ReadAll(io.LimitReader(output.Body, store.MaxProjectContentVersionBytes+1))
	closeErr := output.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("reading project asset object %q: %w", key, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing project asset object %q: %w", key, closeErr)
	}
	if len(body) > store.MaxProjectContentVersionBytes {
		return nil, fmt.Errorf("project asset object %q exceeds the %d-byte limit", key, store.MaxProjectContentVersionBytes)
	}
	return body, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("deleting project asset object %q from S3 bucket %q: %w", key, s.bucket, err)
	}
	return nil
}

type apiError interface {
	error
	ErrorCode() string
}

func isNotFound(err error) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound")
}

var _ store.ProjectContentBlobStore = (*S3)(nil)
