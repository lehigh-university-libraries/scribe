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
)

const (
	bucketEnv = "SCRIBE_UPLOADS_BUCKET"
	prefixEnv = "SCRIBE_UPLOADS_PREFIX"
)

type Attrs struct {
	Updated     time.Time
	Size        int64
	ContentType string
}

var (
	clientOnce sync.Once
	client     *storage.Client
	clientErr  error
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
	r, err := handle.NewReader(ctx)
	if err != nil {
		return nil, Attrs{}, fmt.Errorf("open gs://%s/%s: %w", bucket(), obj, err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, Attrs{}, fmt.Errorf("read gs://%s/%s: %w", bucket(), obj, err)
	}
	return data, Attrs{
		Updated:     attrs.Updated,
		Size:        attrs.Size,
		ContentType: attrs.ContentType,
	}, nil
}

func objectName(name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" || filepath.Base(clean) != clean || strings.ContainsAny(clean, `/\`) {
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
	clientOnce.Do(func() {
		client, clientErr = storage.NewClient(ctx)
	})
	if clientErr != nil {
		return nil, fmt.Errorf("create storage client: %w", clientErr)
	}
	return client, nil
}
