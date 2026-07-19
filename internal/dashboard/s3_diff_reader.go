package dashboard

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3DiffReader fetches and caches unified diff content from S3.
// Caching is safe because completed task diffs are immutable; the cache is
// bounded with FIFO eviction.
type s3DiffReader struct {
	client *s3.Client
	mu     sync.RWMutex
	cache  map[string]string // s3URL -> diff text
	order  []string
}

// maxS3DiffCacheEntries bounds the number of cached diffs.
const maxS3DiffCacheEntries = 128

func newS3DiffReader(ar *s3ActivityReader) *s3DiffReader {
	if ar == nil {
		return nil
	}
	return &s3DiffReader{client: ar.client, cache: make(map[string]string)}
}

// FetchDiff downloads the diff from S3. The s3URL must be in s3://bucket/key form.
// Results are cached after the first successful fetch.
func (r *s3DiffReader) FetchDiff(ctx context.Context, s3URL string) (string, error) {
	r.mu.RLock()
	if cached, ok := r.cache[s3URL]; ok {
		r.mu.RUnlock()
		return cached, nil
	}
	r.mu.RUnlock()

	bucket, key, err := parseS3URL(s3URL)
	if err != nil {
		return "", err
	}

	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("S3 GetObject %s: %w", s3URL, err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return "", fmt.Errorf("reading S3 body %s: %w", s3URL, err)
	}

	diff := string(data)
	r.mu.Lock()
	if _, exists := r.cache[s3URL]; !exists {
		for len(r.order) >= maxS3DiffCacheEntries {
			oldest := r.order[0]
			r.order = r.order[1:]
			delete(r.cache, oldest)
		}
		r.order = append(r.order, s3URL)
	}
	r.cache[s3URL] = diff
	r.mu.Unlock()

	return diff, nil
}
