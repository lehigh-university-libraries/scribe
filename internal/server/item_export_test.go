package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

const testItemExportSigningKey = "test-item-export-signing-key-32-bytes-minimum"

func testItemExportTokenCodec(t *testing.T) *itemExportTokenCodec {
	t.Helper()
	codec, err := newItemExportTokenCodec(testItemExportSigningKey)
	if err != nil {
		t.Fatalf("new item export token codec: %v", err)
	}
	return codec
}

func TestItemExportTokenIsWorkspaceRevisionAndExpiryBound(t *testing.T) {
	codec := testItemExportTokenCodec(t)
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	codec.now = func() time.Time { return now }
	plan := canonicalItemExportPlan{
		Item:   store.Item{ID: "item-export-token", Name: "Token item"},
		Format: "txt",
		Pages: []canonicalExportPage{
			{Image: store.ItemImage{ID: 41}, Page: store.AnnotationPage{Revision: 7}},
			{Image: store.ItemImage{ID: 42}, Page: store.AnnotationPage{Revision: 9}},
		},
	}
	plan.Digest = itemExportRevisionDigest(plan.Pages)
	token, expiresAt, err := codec.encode(11, plan)
	if err != nil {
		t.Fatalf("encode item export token: %v", err)
	}
	if expiresAt.Sub(now) != itemExportTokenTTL || len(token) > maxItemExportTokenBytes {
		t.Fatalf("token expiry/length = %s/%d", expiresAt, len(token))
	}
	decoded, err := codec.decode(token, 11)
	if err != nil || decoded.ItemID != plan.Item.ID || decoded.Format != plan.Format || decoded.Digest != plan.Digest {
		t.Fatalf("decode item export token = %+v/%v", decoded, err)
	}
	if _, err := codec.decode(token, 12); err == nil {
		t.Fatal("workspace-replayed item export token was accepted")
	}
	tampered := token[:len(token)-1] + map[bool]string{true: "A", false: "B"}[strings.HasSuffix(token, "B")]
	if _, err := codec.decode(tampered, 11); err == nil {
		t.Fatal("tampered item export token was accepted")
	}
	codec.now = func() time.Time { return expiresAt.Add(time.Nanosecond) }
	if _, err := codec.decode(token, 11); err == nil {
		t.Fatal("expired item export token was accepted")
	}
}

func TestAnnotationExportFormatContractRejectsUnknownValues(t *testing.T) {
	for _, value := range []scribev1.AnnotationExportFormat{
		scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_UNSPECIFIED,
		scribev1.AnnotationExportFormat(999),
	} {
		if _, err := annotationExportFormatName(value); err == nil {
			t.Fatalf("annotationExportFormatName(%d) accepted an unsupported value", value)
		}
	}
	for _, value := range []scribev1.AnnotationExportFormat{
		scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT,
		scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_HOCR,
		scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PAGE_XML,
		scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_ALTO_XML,
	} {
		if _, err := annotationExportFormatName(value); err != nil {
			t.Fatalf("annotationExportFormatName(%d): %v", value, err)
		}
	}
}

func TestExportConcurrencyPolicyIsGlobalAndWorkspaceScoped(t *testing.T) {
	limiter := newBodyConcurrencyLimiter(maxConcurrentExports, maxConcurrentExportsPerWorkspace)
	releaseA1, okA1 := limiter.TryAcquire("workspace:1")
	releaseA2, okA2 := limiter.TryAcquire("workspace:1")
	if !okA1 || !okA2 {
		t.Fatal("workspace could not acquire its configured export slots")
	}
	if _, allowed := limiter.TryAcquire("workspace:1"); allowed {
		t.Fatal("workspace exceeded its export concurrency limit")
	}
	releaseB1, okB1 := limiter.TryAcquire("workspace:2")
	releaseB2, okB2 := limiter.TryAcquire("workspace:2")
	if !okB1 || !okB2 {
		t.Fatal("second workspace could not use remaining global export capacity")
	}
	if _, allowed := limiter.TryAcquire("workspace:3"); allowed {
		t.Fatal("global export concurrency limit was exceeded")
	}
	releaseA1()
	if releaseC, allowed := limiter.TryAcquire("workspace:3"); !allowed {
		t.Fatal("released export capacity was not reusable")
	} else {
		releaseC()
	}
	releaseA2()
	releaseB1()
	releaseB2()
}

func TestItemExportSourceBudgetRejectsOversizedPlansBeforeRendering(t *testing.T) {
	revisions := []store.AnnotationPageRevision{
		{ItemImageID: 1, Revision: 1, PayloadBytes: 3},
		{ItemImageID: 2, Revision: 1, PayloadBytes: 4},
	}
	if err := enforceItemExportSourceLimit(revisions, 7); err != nil {
		t.Fatalf("exact source-byte budget rejected: %v", err)
	}
	if err := enforceItemExportSourceLimit(revisions, 6); !errors.Is(err, errItemExportSourceLimit) {
		t.Fatalf("oversized source-byte plan = %v, want source limit", err)
	}
	if err := enforceItemExportSourceLimit(revisions, 0); !errors.Is(err, errItemExportInvalid) {
		t.Fatalf("nonpositive source-byte budget = %v, want invalid", err)
	}
}

func TestItemExportStagingEnforcesCancellationOutputAndCleanup(t *testing.T) {
	plan := canonicalItemExportPlan{
		Item:   store.Item{ID: "staging", Name: "Staging"},
		Format: "txt",
		Pages: []canonicalExportPage{
			{Image: store.ItemImage{ID: 1, Sequence: 1}, Page: store.AnnotationPage{Revision: 1}},
			{Image: store.ItemImage{ID: 2, Sequence: 2}, Page: store.AnnotationPage{Revision: 1}},
		},
	}

	t.Run("cancellation during render", func(t *testing.T) {
		tempRoot := t.TempDir()
		t.Setenv("TMPDIR", tempRoot)
		ctx, cancel := context.WithCancel(context.Background())
		renders := 0
		_, cleanup, err := stageCanonicalItemExportWithRenderer(ctx, plan, 32, func(canonicalExportPage, string) (string, string, string, error) {
			renders++
			cancel()
			return "page", "text/plain", "txt", nil
		})
		if !errors.Is(err, context.Canceled) || cleanup != nil || renders != 1 {
			t.Fatalf("canceled staging = cleanup %v/renders %d/error %v", cleanup != nil, renders, err)
		}
		assertEmptyExportTempRoot(t, tempRoot)
	})

	t.Run("aggregate output limit", func(t *testing.T) {
		tempRoot := t.TempDir()
		t.Setenv("TMPDIR", tempRoot)
		renders := 0
		_, cleanup, err := stageCanonicalItemExportWithRenderer(context.Background(), plan, 7, func(canonicalExportPage, string) (string, string, string, error) {
			renders++
			return "1234", "text/plain", "txt", nil
		})
		if !errors.Is(err, errItemExportOutputLimit) || cleanup != nil || renders != 2 {
			t.Fatalf("output-limited staging = cleanup %v/renders %d/error %v", cleanup != nil, renders, err)
		}
		assertEmptyExportTempRoot(t, tempRoot)
	})

	t.Run("successful file is unlinked immediately", func(t *testing.T) {
		tempRoot := t.TempDir()
		t.Setenv("TMPDIR", tempRoot)
		staged, cleanup, err := stageCanonicalItemExportWithRenderer(context.Background(), plan, 32, func(page canonicalExportPage, _ string) (string, string, string, error) {
			return map[uint64]string{1: "one", 2: "two"}[page.Image.ID], "text/plain", "txt", nil
		})
		if err != nil {
			t.Fatalf("stage valid export: %v", err)
		}
		defer cleanup()
		body, err := io.ReadAll(staged.File)
		if err != nil || string(body) != "one\n\ntwo" || staged.Size != int64(len(body)) {
			t.Fatalf("staged body = %q/%d/%v", body, staged.Size, err)
		}
		assertEmptyExportTempRoot(t, tempRoot)
	})

	t.Run("temporary storage failure is typed", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir()+"/missing")
		_, cleanup, err := stageCanonicalItemExportWithRenderer(context.Background(), plan, 32, func(canonicalExportPage, string) (string, string, string, error) {
			return "page", "text/plain", "txt", nil
		})
		if !errors.Is(err, errItemExportStaging) || cleanup != nil {
			t.Fatalf("storage failure = cleanup %v/error %v", cleanup != nil, err)
		}
	})
}

func assertEmptyExportTempRoot(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("export temp root contains %d entries: %v", len(entries), err)
	}
}

type deadlineResponseWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestExportDeadlineReachesAccessLoggerResponseWriter(t *testing.T) {
	underlying := &deadlineResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	wrapper := &responseWriter{ResponseWriter: underlying}
	request := httptest.NewRequest(http.MethodGet, "/v1/item-exports/token", nil)
	ctx, finish := exportRequestContext(wrapper, request)
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > maxPreparedExportDuration {
		t.Fatalf("export context deadline = %v/%t", deadline, ok)
	}
	finish()
	if len(underlying.deadlines) != 2 || underlying.deadlines[0].IsZero() || !underlying.deadlines[1].IsZero() {
		t.Fatalf("write deadlines = %#v, want applied then reset", underlying.deadlines)
	}
}
