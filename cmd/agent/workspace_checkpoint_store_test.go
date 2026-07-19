package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakeWorkspaceS3Client struct {
	putInput   *s3.PutObjectInput
	getOutput  *s3.GetObjectOutput
	getErr     error
	putErr     error
	deleteErr  error
	deletedKey string
}

func (f *fakeWorkspaceS3Client) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putInput = input
	return &s3.PutObjectOutput{}, f.putErr
}

func (f *fakeWorkspaceS3Client) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return f.getOutput, f.getErr
}

func (f *fakeWorkspaceS3Client) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deletedKey = aws.ToString(input.Key)
	return &s3.DeleteObjectOutput{}, f.deleteErr
}

type fakeWorkspaceAPIError struct{ code string }

func (e fakeWorkspaceAPIError) Error() string     { return e.code }
func (e fakeWorkspaceAPIError) ErrorCode() string { return e.code }

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestWorkspaceObjectStoreGetDistinguishesMissingFromFailure(t *testing.T) {
	for _, code := range []string{"NoSuchKey", "NotFound"} {
		client := &fakeWorkspaceS3Client{getErr: fakeWorkspaceAPIError{code: code}}
		store := &s3WorkspaceObjectStore{client: client, bucket: "bucket"}
		if body, found, err := store.Get(context.Background(), "key"); err != nil || found || body != nil {
			t.Fatalf("%s: body=%v found=%v err=%v", code, body, found, err)
		}
	}
	client := &fakeWorkspaceS3Client{getErr: fakeWorkspaceAPIError{code: "AccessDenied"}}
	store := &s3WorkspaceObjectStore{client: client, bucket: "bucket"}
	if _, found, err := store.Get(context.Background(), "key"); err == nil || found {
		t.Fatalf("AccessDenied: found=%v err=%v", found, err)
	}
}

func TestWorkspaceObjectStoreGetBoundsAndClosesBody(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("payload")}
	client := &fakeWorkspaceS3Client{getOutput: &s3.GetObjectOutput{Body: body, ContentLength: aws.Int64(7)}}
	store := &s3WorkspaceObjectStore{client: client, bucket: "bucket"}
	got, found, err := store.Get(context.Background(), "key")
	if err != nil || !found || string(got) != "payload" {
		t.Fatalf("got=%q found=%v err=%v", got, found, err)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}

	oversized := &trackingReadCloser{Reader: bytes.NewReader(nil)}
	client.getOutput = &s3.GetObjectOutput{Body: oversized, ContentLength: aws.Int64(maxWorkspaceObjectBytes + 1)}
	if _, found, err := store.Get(context.Background(), "large"); err == nil || !found {
		t.Fatalf("oversized object: found=%v err=%v", found, err)
	}
	if !oversized.closed {
		t.Fatal("oversized response body was not closed")
	}
}

func TestWorkspaceObjectStorePutAndDelete(t *testing.T) {
	client := &fakeWorkspaceS3Client{}
	store := &s3WorkspaceObjectStore{client: client, bucket: "bucket"}
	payload := []byte("checkpoint")
	if err := store.Put(context.Background(), "run/object", payload); err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(client.putInput.Bucket); got != "bucket" {
		t.Fatalf("bucket = %q", got)
	}
	if got := aws.ToString(client.putInput.Key); got != "run/object" {
		t.Fatalf("key = %q", got)
	}
	if got := aws.ToInt64(client.putInput.ContentLength); got != int64(len(payload)) {
		t.Fatalf("content length = %d", got)
	}
	body, err := io.ReadAll(client.putInput.Body)
	if err != nil || !bytes.Equal(body, payload) {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if aws.ToString(client.putInput.ChecksumCRC32) == "" {
		t.Fatal("CRC32 checksum was not set")
	}
	if err := store.Delete(context.Background(), "run/latest"); err != nil {
		t.Fatal(err)
	}
	if client.deletedKey != "run/latest" {
		t.Fatalf("deleted key = %q", client.deletedKey)
	}

	client.putErr = errors.New("down")
	if err := store.Put(context.Background(), "run/object", payload); err == nil || !strings.Contains(err.Error(), "run/object") {
		t.Fatalf("put error = %v", err)
	}
	client.deleteErr = errors.New("down")
	if err := store.Delete(context.Background(), "run/latest"); err == nil || !strings.Contains(err.Error(), "run/latest") {
		t.Fatalf("delete error = %v", err)
	}
}

func TestWorkspaceObjectStoreConfigurationValidation(t *testing.T) {
	t.Setenv("S3_BUCKET", "")
	if _, _, err := newWorkspaceObjectStoreFromEnv(); err == nil {
		t.Fatal("missing S3_BUCKET was accepted")
	}
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("AWS_ACCESS_KEY_ID", "only-one")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if _, _, err := newWorkspaceObjectStoreFromEnv(); err == nil {
		t.Fatal("partial static credentials were accepted")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("S3_ENDPOINT", "not-absolute")
	if _, _, err := newWorkspaceObjectStoreFromEnv(); err == nil {
		t.Fatal("relative S3 endpoint was accepted")
	}
	t.Setenv("S3_ENDPOINT", "http://minio.test:9000")
	store, prefix, err := newWorkspaceObjectStoreFromEnv()
	if err != nil || store == nil || prefix != workspaceCheckpointStorePrefix {
		t.Fatalf("store=%T prefix=%q err=%v", store, prefix, err)
	}
}
