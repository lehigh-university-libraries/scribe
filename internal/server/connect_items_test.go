package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestUploadBatchFailureStagesExposeOnlyFixedCategories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		stage uploadBatchFailureStage
		err   error
		want  string
	}{
		{name: "admission", stage: uploadBatchFailureAdmission, want: "admission failed"},
		{name: "storage override", stage: uploadBatchFailureSegmentationOutput, err: ocrhandlers.ErrUploadStorageFailure, want: "upload storage failed"},
		{name: "provider override", stage: uploadBatchFailureImageCommit, err: providers.NewError(providers.ErrorAuthentication, http.StatusForbidden, false, nil), want: "provider request failed with HTTP status 403"},
		{name: "provider override wins over joined processing error", stage: uploadBatchFailureAnnotationCommit, err: errors.Join(ocrhandlers.ErrSegmentationOutputFailure, providers.NewError(providers.ErrorAuthentication, http.StatusForbidden, false, nil)), want: "provider request failed with HTTP status 403"},
		{name: "segmentation output", stage: uploadBatchFailureSegmentationOutput, want: "segmentation output failed"},
		{name: "typed segmentation override", stage: uploadBatchFailureAdmission, err: ocrhandlers.ErrSegmentationOutputFailure, want: "segmentation output failed"},
		{name: "quota resize", stage: uploadBatchFailureQuotaResize, want: "quota resize failed"},
		{name: "lease renewal", stage: uploadBatchFailureLeaseRenewal, want: "lease renewal failed"},
		{name: "image commit", stage: uploadBatchFailureImageCommit, want: "image commit failed"},
		{name: "ocr run commit", stage: uploadBatchFailureOCRRunCommit, want: "ocr run commit failed"},
		{name: "annotation commit", stage: uploadBatchFailureAnnotationCommit, want: "annotation commit failed"},
		{name: "transcription enqueue", stage: uploadBatchFailureTranscriptionEnqueue, want: "transcription enqueue failed"},
		{name: "item reload", stage: uploadBatchFailureItemReload, want: "item reload failed"},
		{name: "batch commit", stage: uploadBatchFailureBatchCommit, want: "batch commit failed"},
		{name: "unknown stage fails closed", stage: uploadBatchFailureStage(255), err: errors.New("private database detail"), want: "admission failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := uploadBatchFailureMessage(test.stage, test.err); got != test.want {
				t.Fatalf("uploadBatchFailureMessage() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUploadBatchLeaseFailureOverridesCurrentOperationStage(t *testing.T) {
	t.Parallel()

	const current = "annotation commit failed"
	if got := uploadBatchFailureAfterLeaseRenewal(current, nil); got != current {
		t.Fatalf("successful lease renewal changed failure stage to %q", got)
	}
	if got := uploadBatchFailureAfterLeaseRenewal(current, errors.New("private lease diagnostic")); got != "lease renewal failed" {
		t.Fatalf("lease renewal failure stage = %q, want lease renewal failed", got)
	}
}

func TestItemNameAndMetadataAdmissionLimits(t *testing.T) {
	t.Parallel()

	if err := validateItemName(strings.Repeat("é", maxItemNameRunes)); err != nil {
		t.Fatalf("exact item name limit rejected: %v", err)
	}
	if err := validateItemName(strings.Repeat("é", maxItemNameRunes+1)); err == nil {
		t.Fatal("oversized item name accepted")
	}
	if got, err := normalizeItemMetadata(""); err != nil || got != "{}" {
		t.Fatalf("empty metadata = %q, %v; want {}", got, err)
	}
	if got, err := normalizeItemMetadata(` { "source": "test" } `); err != nil || got != `{"source":"test"}` {
		t.Fatalf("object metadata = %q, %v", got, err)
	}
	for _, invalid := range []string{"null", `[]`, strings.Repeat("x", maxItemMetadataBytes+1)} {
		if _, err := normalizeItemMetadata(invalid); err == nil {
			t.Errorf("normalizeItemMetadata accepted invalid input prefix %q", invalid[:min(len(invalid), 16)])
		}
	}

	handler := &Handler{}
	_, err := handler.ImportManifest(context.Background(), connect.NewRequest(&scribev1.ImportManifestRequest{
		Name:           strings.Repeat("x", maxItemNameRunes+1),
		ManifestUrl:    "https://iiif.example.test/manifest",
		IdempotencyKey: "oversized-name",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversized ImportManifest name error = %v, want invalid_argument", err)
	}
	_, err = handler.ImportManifest(context.Background(), connect.NewRequest(&scribev1.ImportManifestRequest{
		ManifestUrl:    "https://iiif.example.test/manifest",
		Metadata:       "null",
		IdempotencyKey: "null-metadata",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("null ImportManifest metadata error = %v, want invalid_argument", err)
	}
}

func TestUploadBatchLeaseHeartbeatCancelsProcessingOnFence(t *testing.T) {
	t.Parallel()

	ticks := make(chan time.Time)
	renewed := make(chan struct{}, 2)
	leaseFence := errors.New("lease fenced")
	callCount := 0
	leaseCtx, stop, failures := newUploadBatchLeaseHeartbeat(context.Background(), ticks, func(context.Context) error {
		callCount++
		renewed <- struct{}{}
		if callCount == 2 {
			return leaseFence
		}
		return nil
	})
	defer stop()

	ticks <- time.Now()
	<-renewed
	select {
	case <-leaseCtx.Done():
		t.Fatal("successful lease renewal canceled processing")
	default:
	}

	ticks <- time.Now()
	<-renewed
	select {
	case err := <-failures:
		if !errors.Is(err, leaseFence) {
			t.Fatalf("heartbeat failure = %v, want lease fence", err)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not report renewal failure")
	}
	select {
	case <-leaseCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not cancel processing after renewal failure")
	}
}

func TestUploadNameFromURL(t *testing.T) {
	t.Parallel()
	immutableName := strings.Repeat("a", 64) + "-12345678-1234-4123-8123-123456789abc.jpg"

	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "immutable application upload", raw: "/static/uploads/" + immutableName, want: immutableName, ok: true},
		{name: "noncanonical application upload", raw: "/static/uploads/image-123.jpg"},
		{name: "remote host cannot delete local upload", raw: "https://attacker.example/static/uploads/image-123.jpg"},
		{name: "scheme relative host cannot delete local upload", raw: "//attacker.example/static/uploads/image-123.jpg"},
		{name: "query is not a stored object identity", raw: "/static/uploads/image-123.jpg?download=1"},
		{name: "fragment is not a stored object identity", raw: "/static/uploads/image-123.jpg#fragment"},
		{name: "path traversal", raw: "/static/uploads/../secret"},
		{name: "nested object", raw: "/static/uploads/nested/image.jpg"},
		{name: "other static resource", raw: "/static/image-123.jpg"},
		{name: "empty", raw: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := uploadNameFromURL(test.raw)
			if ok != test.ok || got != test.want {
				t.Fatalf("uploadNameFromURL(%q) = (%q, %t), want (%q, %t)", test.raw, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestNormalizeUploadBatchRequestBindsContextAndOrderedContent(t *testing.T) {
	t.Parallel()

	request := &scribev1.StartUploadBatchRequest{
		BatchId:   "batch-1",
		Name:      "Bound volume",
		ContextId: 7,
		Files: []*scribev1.UploadBatchFileInput{
			{Filename: "page-1.png", Size: 5, ContentSha256: strings.Repeat("a", 64)},
			{Filename: "page-2.png", Size: 6, ContentSha256: strings.Repeat("b", 64)},
		},
	}
	files, digest, err := normalizeUploadBatchRequest(request)
	if err != nil {
		t.Fatalf("normalizeUploadBatchRequest: %v", err)
	}
	if len(files) != 2 || files[0].Filename != "page-1.png" || len(digest) != 64 {
		t.Fatalf("normalized files/digest = %+v/%q", files, digest)
	}
	request.ContextId = 8
	_, changedContextDigest, err := normalizeUploadBatchRequest(request)
	if err != nil {
		t.Fatalf("normalize changed context: %v", err)
	}
	if changedContextDigest == digest {
		t.Fatal("context change did not change upload batch request hash")
	}
	request.ContextId = 7
	request.Files[0].ContentSha256 = strings.Repeat("c", 64)
	_, changedContentDigest, err := normalizeUploadBatchRequest(request)
	if err != nil {
		t.Fatalf("normalize changed content: %v", err)
	}
	if changedContentDigest == digest {
		t.Fatal("content change did not change upload batch request hash")
	}
}

func TestNormalizeUploadBatchRequestEnforcesCostAndFilenameLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []*scribev1.UploadBatchFileInput
	}{
		{name: "path filename", files: []*scribev1.UploadBatchFileInput{{Filename: "../page.png", Size: 1, ContentSha256: strings.Repeat("a", 64)}}},
		{name: "oversized file", files: []*scribev1.UploadBatchFileInput{{Filename: "page.png", Size: maxDeclaredImageBytes + 1, ContentSha256: strings.Repeat("a", 64)}}},
		{name: "invalid digest", files: []*scribev1.UploadBatchFileInput{{Filename: "page.png", Size: 1, ContentSha256: strings.Repeat("A", 64)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := normalizeUploadBatchRequest(&scribev1.StartUploadBatchRequest{BatchId: "batch-1", Files: test.files}); err == nil {
				t.Fatal("normalizeUploadBatchRequest unexpectedly accepted invalid declaration")
			}
		})
	}
}
