package contentblob

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

type fakeS3Client struct {
	putInput    *s3.PutObjectInput
	getOutput   *s3.GetObjectOutput
	getErr      error
	deleteInput *s3.DeleteObjectInput
}

func (f *fakeS3Client) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putInput = input
	return &s3.PutObjectOutput{}, nil
}
func (f *fakeS3Client) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return f.getOutput, f.getErr
}
func (f *fakeS3Client) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleteInput = input
	return &s3.DeleteObjectOutput{}, nil
}

type fakeAPIError struct{ code string }

func (e fakeAPIError) Error() string     { return e.code }
func (e fakeAPIError) ErrorCode() string { return e.code }

func TestS3PutSetsChecksumAndContentType(t *testing.T) {
	client := &fakeS3Client{}
	blobs := &S3{client: client, bucket: "assets"}
	if err := blobs.Put(context.Background(), "key", []byte("body"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if client.putInput == nil || client.putInput.ChecksumCRC32 == nil || client.putInput.ContentType == nil || *client.putInput.ContentType != "image/png" {
		t.Fatalf("PutObject input = %#v", client.putInput)
	}
	got, err := io.ReadAll(client.putInput.Body)
	if err != nil || string(got) != "body" {
		t.Fatalf("uploaded body = %q, %v", got, err)
	}
}

func TestS3GetReturnsBytesAndMapsMissingObject(t *testing.T) {
	length := int64(4)
	client := &fakeS3Client{getOutput: &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("body")), ContentLength: &length}}
	blobs := &S3{client: client, bucket: "assets"}
	got, err := blobs.Get(context.Background(), "key")
	if err != nil || string(got) != "body" {
		t.Fatalf("Get() = %q, %v", got, err)
	}
	client.getErr = fakeAPIError{code: "NoSuchKey"}
	client.getOutput = nil
	if _, err := blobs.Get(context.Background(), "missing"); !errors.Is(err, store.ErrContentBlobNotFound) {
		t.Fatalf("missing Get() error = %v", err)
	}
}
