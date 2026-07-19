package main

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
)

const (
	workspaceCheckpointStorePrefix = "workspace-checkpoints/v1"
	maxWorkspaceObjectBytes        = int64(512 << 20)
)

type workspaceObjectStore interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, bool, error)
	Delete(context.Context, string) error
}

type workspaceS3Client interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type s3WorkspaceObjectStore struct {
	client workspaceS3Client
	bucket string
}

func newWorkspaceObjectStoreFromEnv() (workspaceObjectStore, string, error) {
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if bucket == "" {
		return nil, "", fmt.Errorf("S3_BUCKET is required for workspace checkpoints")
	}

	region := strings.TrimSpace(os.Getenv("S3_REGION"))
	if region == "" {
		region = "us-east-1"
	}
	cfg := aws.Config{Region: region}

	accessKeyID := strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID"))
	secretAccessKey := strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY"))
	if (accessKeyID == "") != (secretAccessKey == "") {
		return nil, "", fmt.Errorf("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set together for workspace checkpoints")
	}
	if accessKeyID != "" {
		cfg.Credentials = credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")
	}

	endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		if endpoint == "" {
			return
		}
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
	if endpoint != "" {
		parsed, err := url.ParseRequestURI(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, "", fmt.Errorf("S3_ENDPOINT must be an absolute URL")
		}
	}

	return &s3WorkspaceObjectStore{client: client, bucket: bucket}, workspaceCheckpointStorePrefix, nil
}

func (s *s3WorkspaceObjectStore) Put(ctx context.Context, key string, body []byte) error {
	checksum := crc32.ChecksumIEEE(body)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
		ChecksumCRC32: aws.String(base64.StdEncoding.EncodeToString([]byte{byte(checksum >> 24), byte(checksum >> 16), byte(checksum >> 8), byte(checksum)})),
	})
	if err != nil {
		return fmt.Errorf("putting workspace checkpoint object %q in S3 bucket %q: %w", key, s.bucket, err)
	}
	return nil
}

func (s *s3WorkspaceObjectStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isWorkspaceObjectNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("getting workspace checkpoint object %q from S3 bucket %q: %w", key, s.bucket, err)
	}
	if output.ContentLength != nil && *output.ContentLength > maxWorkspaceObjectBytes {
		_ = output.Body.Close()
		return nil, true, fmt.Errorf("workspace checkpoint object %q in S3 bucket %q is larger than the %d-byte limit", key, s.bucket, maxWorkspaceObjectBytes)
	}

	body, readErr := io.ReadAll(io.LimitReader(output.Body, maxWorkspaceObjectBytes+1))
	closeErr := output.Body.Close()
	if readErr != nil {
		return nil, true, fmt.Errorf("reading workspace checkpoint object %q from S3 bucket %q: %w", key, s.bucket, readErr)
	}
	if closeErr != nil {
		return nil, true, fmt.Errorf("closing workspace checkpoint object %q from S3 bucket %q: %w", key, s.bucket, closeErr)
	}
	if int64(len(body)) > maxWorkspaceObjectBytes {
		return nil, true, fmt.Errorf("workspace checkpoint object %q in S3 bucket %q is larger than the %d-byte limit", key, s.bucket, maxWorkspaceObjectBytes)
	}
	return body, true, nil
}

func (s *s3WorkspaceObjectStore) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("deleting workspace checkpoint object %q from S3 bucket %q: %w", key, s.bucket, err)
	}
	return nil
}

type workspaceS3APIError interface {
	error
	ErrorCode() string
}

func isWorkspaceObjectNotFound(err error) bool {
	var apiErr workspaceS3APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound")
}
