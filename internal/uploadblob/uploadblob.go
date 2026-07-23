package uploadblob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
)

const (
	bucketEnv            = "SCRIBE_UPLOADS_BUCKET"
	prefixEnv            = "SCRIBE_UPLOADS_PREFIX"
	maxStoredUploadBytes = 100 << 20
)

type Attrs struct {
	Updated     time.Time
	Size        int64
	ContentType string
}

var (
	clientMu         sync.Mutex
	client           *storage.Client
	newStorageClient = storage.NewClient
)

func Enabled() bool {
	return strings.TrimSpace(os.Getenv(bucketEnv)) != ""
}

func Put(ctx context.Context, name string, data []byte, contentType string) error {
	if !Enabled() {
		return nil
	}
	obj, err := objectName(name)
	if err != nil {
		return err
	}
	c, err := getClient(ctx)
	if err != nil {
		return err
	}
	w := c.Bucket(bucket()).Object(obj).NewWriter(ctx)
	if contentType != "" {
		w.ContentType = contentType
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return fmt.Errorf("write gs://%s/%s: %w", bucket(), obj, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close gs://%s/%s: %w", bucket(), obj, err)
	}
	return nil
}

func Read(ctx context.Context, name string) ([]byte, Attrs, error) {
	if !Enabled() {
		return nil, Attrs{}, fmt.Errorf("shared upload bucket is not configured")
	}
	obj, err := objectName(name)
	if err != nil {
		return nil, Attrs{}, err
	}
	c, err := getClient(ctx)
	if err != nil {
		return nil, Attrs{}, err
	}
	handle := c.Bucket(bucket()).Object(obj)
	attrs, err := handle.Attrs(ctx)
	if err != nil {
		return nil, Attrs{}, fmt.Errorf("stat gs://%s/%s: %w", bucket(), obj, err)
	}
	if attrs.Size < 0 || attrs.Size > maxStoredUploadBytes {
		return nil, Attrs{}, fmt.Errorf("gs://%s/%s exceeds the %d-byte read limit", bucket(), obj, maxStoredUploadBytes)
	}
	r, err := handle.NewReader(ctx)
	if err != nil {
		return nil, Attrs{}, fmt.Errorf("open gs://%s/%s: %w", bucket(), obj, err)
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxStoredUploadBytes+1))
	if err != nil {
		return nil, Attrs{}, fmt.Errorf("read gs://%s/%s: %w", bucket(), obj, err)
	}
	if len(data) > maxStoredUploadBytes {
		return nil, Attrs{}, fmt.Errorf("gs://%s/%s exceeds the %d-byte read limit", bucket(), obj, maxStoredUploadBytes)
	}
	return data, Attrs{
		Updated:     attrs.Updated,
		Size:        attrs.Size,
		ContentType: attrs.ContentType,
	}, nil
}

// Delete removes one validated upload object. Missing objects are already in
// the desired state and therefore do not fail cleanup.
func Delete(ctx context.Context, name string) error {
	if !Enabled() {
		return nil
	}
	obj, err := objectName(name)
	if err != nil {
		return err
	}
	c, err := getClient(ctx)
	if err != nil {
		return err
	}
	if err := c.Bucket(bucket()).Object(obj).Delete(ctx); err != nil && err != storage.ErrObjectNotExist {
		return fmt.Errorf("delete gs://%s/%s: %w", bucket(), obj, err)
	}
	return nil
}

func objectName(name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" || filepath.Base(clean) != clean || strings.ContainsAny(clean, `/\`) || !uploadref.IsImmutableName(clean) {
		return "", fmt.Errorf("invalid upload object name %q", name)
	}
	prefix := strings.Trim(strings.TrimSpace(os.Getenv(prefixEnv)), "/")
	if prefix == "" {
		return clean, nil
	}
	return prefix + "/" + clean, nil
}

func bucket() string {
	return strings.TrimSpace(os.Getenv(bucketEnv))
}

func getClient(ctx context.Context) (*storage.Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	clientMu.Lock()
	defer clientMu.Unlock()
	if client != nil {
		return client, nil
	}
	created, err := newStorageClient(ctx)
	if err != nil {
		// Initialization failures are intentionally not cached. A canceled
		// startup request or transient credential outage must not poison the
		// process for the remainder of its lifetime.
		return nil, fmt.Errorf("create storage client: %w", err)
	}
	client = created
	return created, nil
}

// Close releases the process-owned shared upload client. It is idempotent and
// permits a later call to initialize a fresh client.
func Close() error {
	clientMu.Lock()
	defer clientMu.Unlock()
	if client == nil {
		return nil
	}
	err := client.Close()
	client = nil
	if err != nil {
		return fmt.Errorf("close storage client: %w", err)
	}
	return nil
}
