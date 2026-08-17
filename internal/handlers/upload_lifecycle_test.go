package handlers

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/utils"
)

type fakeUploadObjectStore struct {
	mu            sync.Mutex
	objects       map[string][]byte
	contentTypes  map[string]string
	putErr        error
	deleteErr     error
	deleteStarted chan struct{}
	allowDelete   chan struct{}
	deleteOnce    sync.Once
	beforePut     func()
}

func newFakeUploadObjectStore() *fakeUploadObjectStore {
	return &fakeUploadObjectStore{
		objects:      make(map[string][]byte),
		contentTypes: make(map[string]string),
	}
}

func (s *fakeUploadObjectStore) Put(_ context.Context, name string, data []byte, contentType string) error {
	if s.beforePut != nil {
		s.beforePut()
	}
	s.mu.Lock()
	s.objects[name] = append([]byte(nil), data...)
	s.contentTypes[name] = contentType
	err := s.putErr
	s.mu.Unlock()
	return err
}

func (s *fakeUploadObjectStore) Delete(_ context.Context, name string) error {
	if s.deleteStarted != nil {
		s.deleteOnce.Do(func() { close(s.deleteStarted) })
	}
	if s.allowDelete != nil {
		<-s.allowDelete
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.objects, name)
	return nil
}

func (s *fakeUploadObjectStore) has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[name]
	return ok
}

func (s *fakeUploadObjectStore) contentType(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contentTypes[name]
}

type failingHOCRProcessor struct {
	err error
}

type successfulHOCRProcessor struct{}

func (*successfulHOCRProcessor) SetProviderCallAuditLogger(hocr.ProviderCallAuditLogger) {}

func (*successfulHOCRProcessor) TranscribeImageWithContext(context.Context, string, string, string) (string, error) {
	return "", errors.New("unexpected transcription call")
}

func (*successfulHOCRProcessor) ProcessImageWithContext(context.Context, string, hocr.ProcessingContext) (string, string, string, error) {
	return `<html><body><span class="ocr_line" title="bbox 0 0 2 2"><span class="ocrx_word" title="bbox 0 0 2 2">ok</span></span></body></html>`, "test", "test", nil
}

func (*failingHOCRProcessor) SetProviderCallAuditLogger(hocr.ProviderCallAuditLogger) {}

func (*failingHOCRProcessor) TranscribeImageWithContext(context.Context, string, string, string) (string, error) {
	return "", errors.New("unexpected transcription call")
}

func (p *failingHOCRProcessor) ProcessImageWithContext(context.Context, string, hocr.ProcessingContext) (string, string, string, error) {
	return "", "", "", p.err
}

func TestUploadIsDurablyStagedBeforeAnyBlobWrite(t *testing.T) {
	withUploadWorkingDirectory(t)
	payload := testPNG(t)
	objects := newFakeUploadObjectStore()
	staged := false
	handler := &Handler{uploadObjects: objects}
	handler.SetUploadStager(func(_ context.Context, imageURL string, storageBytes uint64) error {
		name, ok := uploadNameForTest(imageURL)
		if !ok {
			t.Fatalf("staged upload URL = %q", imageURL)
		}
		if storageBytes != uint64(len(payload)) {
			t.Fatalf("staged bytes = %d, want %d", storageBytes, len(payload))
		}
		if _, err := os.Stat(filepath.Join("uploads", name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("local blob existed before durable staging: %v", err)
		}
		staged = true
		return nil
	})
	objects.beforePut = func() {
		if !staged {
			t.Fatal("object-store Put ran before durable staging")
		}
	}
	if _, err := handler.StoreUploadedImage(context.Background(), "page.png", payload); err != nil {
		t.Fatalf("StoreUploadedImage: %v", err)
	}
}

func TestCleanupStaleUploadTempsIsConservative(t *testing.T) {
	uploadDir := t.TempDir()
	now := time.Now().UTC()
	oldTemp := filepath.Join(uploadDir, ".scribe-upload-old")
	recentTemp := filepath.Join(uploadDir, ".scribe-upload-recent")
	canonical := filepath.Join(uploadDir, "page.jpg")
	for _, path := range []string{oldTemp, recentTemp, canonical} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(oldTemp, now.Add(-2*UploadTempRecoveryAge), now.Add(-2*UploadTempRecoveryAge)); err != nil {
		t.Fatal(err)
	}
	if err := CleanupStaleUploadTemps(uploadDir, now, UploadTempRecoveryAge); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldTemp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old temporary stat = %v, want not exist", err)
	}
	for _, retained := range []string{recentTemp, canonical} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("retained file %q: %v", filepath.Base(retained), err)
		}
	}
}

func TestImmutableUploadIdentityPreventsConcurrentCleanupFromDeletingReuse(t *testing.T) {
	withUploadWorkingDirectory(t)
	payload := testPNG(t)
	objects := newFakeUploadObjectStore()
	handler := &Handler{uploadObjects: objects}

	firstURL, err := handler.StoreUploadedImage(context.Background(), "page.png", payload)
	if err != nil {
		t.Fatalf("store first upload: %v", err)
	}
	firstName, ok := uploadNameForTest(firstURL)
	if !ok {
		t.Fatalf("first upload URL = %q", firstURL)
	}
	objects.deleteStarted = make(chan struct{})
	objects.allowDelete = make(chan struct{})
	deleteReleased := false
	defer func() {
		if !deleteReleased {
			close(objects.allowDelete)
		}
	}()
	cleanupDone := make(chan error, 1)
	go func() {
		cleanupDone <- handler.deleteUploadedImage(context.Background(), firstName)
	}()
	<-objects.deleteStarted

	secondURL, err := handler.StoreUploadedImage(context.Background(), "page.png", payload)
	if err != nil {
		t.Fatalf("store identical upload during cleanup: %v", err)
	}
	secondName, ok := uploadNameForTest(secondURL)
	if !ok {
		t.Fatalf("second upload URL = %q", secondURL)
	}
	if firstName == secondName {
		t.Fatalf("identical ingests reused mutable object name %q", firstName)
	}
	wantPrefix := utils.CalculateDataHash(payload) + "-"
	if len(secondName) <= len(wantPrefix) || secondName[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("upload name %q does not preserve digest prefix %q", secondName, wantPrefix)
	}

	close(objects.allowDelete)
	deleteReleased = true
	if err := <-cleanupDone; err != nil {
		t.Fatalf("clean first upload: %v", err)
	}
	if objects.has(firstName) {
		t.Fatal("cleanup left the retired remote object")
	}
	if !objects.has(secondName) {
		t.Fatal("cleanup deleted the concurrently ingested remote object")
	}
	if _, err := os.Stat(filepath.Join("uploads", firstName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired local object stat error = %v, want not exist", err)
	}
	if data, err := os.ReadFile(filepath.Join("uploads", secondName)); err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("concurrent local object = %d bytes/%v, want %d bytes", len(data), err, len(payload))
	}
}

func TestAmbiguousSharedStoreFailureCompensatesLocalAndRemoteWrites(t *testing.T) {
	withUploadWorkingDirectory(t)
	payload := testPNG(t)
	putErr := errors.New("ambiguous shared-store close failure")
	objects := newFakeUploadObjectStore()
	objects.putErr = putErr
	handler := &Handler{uploadObjects: objects}

	_, err := handler.StoreUploadedImage(context.Background(), "page.png", payload)
	if !errors.Is(err, putErr) {
		t.Fatalf("StoreUploadedImage error = %v, want put failure", err)
	}
	if message, ok := SafeUploadProcessingFailureMessage(err); !ok || message != "upload storage failed" {
		t.Fatalf("SafeUploadProcessingFailureMessage() = %q, %t; want fixed storage category", message, ok)
	}
	imageURL, storedBytes, ok := StoredUploadDetails(err)
	if !ok {
		t.Fatalf("failure did not retain durable cleanup identity: %v", err)
	}
	if storedBytes != uint64(len(payload)) {
		t.Fatalf("failure stored bytes = %d, want %d", storedBytes, len(payload))
	}
	name, ok := uploadNameForTest(imageURL)
	if !ok {
		t.Fatalf("cleanup URL = %q", imageURL)
	}
	if objects.has(name) {
		t.Fatal("ambiguous shared-store write was not compensated")
	}
	if _, err := os.Stat(filepath.Join("uploads", name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local upload stat error = %v, want not exist", err)
	}
	assertNoUploadTemps(t)
}

func TestPostWriteProcessingFailureCompensatesBothStores(t *testing.T) {
	withUploadWorkingDirectory(t)
	payload := testPNG(t)
	processingErr := errors.New("segmentor unavailable")
	objects := newFakeUploadObjectStore()
	handler := &Handler{
		hocrService:   &failingHOCRProcessor{err: processingErr},
		uploadObjects: objects,
	}

	_, err := handler.ProcessImageUploadWithContext(context.Background(), "page.png", payload, hocr.ProcessingContext{})
	if !errors.Is(err, processingErr) {
		t.Fatalf("ProcessImageUploadWithContext error = %v, want processing failure", err)
	}
	if message, ok := SafeUploadProcessingFailureMessage(err); !ok || message != "segmentation output failed" {
		t.Fatalf("SafeUploadProcessingFailureMessage() = %q, %t; want fixed segmentation-output category", message, ok)
	}
	if message, ok := SafeUploadProcessingFailureMessage(errors.New("private parser detail")); ok || message != "" {
		t.Fatalf("unclassified error exposed as %q, %t", message, ok)
	}
	imageURL, storedBytes, ok := StoredUploadDetails(err)
	if !ok {
		t.Fatalf("processing failure did not retain cleanup identity: %v", err)
	}
	if storedBytes != uint64(len(payload)) {
		t.Fatalf("processing failure stored bytes = %d, want %d", storedBytes, len(payload))
	}
	name, ok := uploadNameForTest(imageURL)
	if !ok {
		t.Fatalf("cleanup URL = %q", imageURL)
	}
	if objects.has(name) {
		t.Fatal("processing failure left the remote upload")
	}
	if _, err := os.Stat(filepath.Join("uploads", name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("processing failure local stat error = %v, want not exist", err)
	}
	assertNoUploadTemps(t)
}

func TestLocalOwnedUploadReportsExactPositiveStoredByteCount(t *testing.T) {
	withUploadWorkingDirectory(t)
	payload := testPNG(t)
	objects := newFakeUploadObjectStore()
	handler := &Handler{
		hocrService:   &successfulHOCRProcessor{},
		uploadObjects: objects,
	}

	result, err := handler.ProcessImageUploadWithContext(context.Background(), "page.png", payload, hocr.ProcessingContext{})
	if err != nil {
		t.Fatalf("ProcessImageUploadWithContext: %v", err)
	}
	if result.StoredBytes != uint64(len(payload)) {
		t.Fatalf("stored bytes = %d, want %d", result.StoredBytes, len(payload))
	}
	if result.StoredBytes == 0 {
		t.Fatal("Scribe-owned upload reported zero stored bytes")
	}
	name, ok := uploadNameForTest(result.ImageURL)
	if !ok || !objects.has(name) {
		t.Fatalf("successful upload URL/object = %q/%t", result.ImageURL, objects.has(name))
	}
}

func TestRemoteDeclaredContentTypeCannotBecomeStoredObjectMetadata(t *testing.T) {
	withUploadWorkingDirectory(t)
	payload := testPNG(t)
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()
	processingErr := errors.New("stop after storage metadata capture")
	objects := newFakeUploadObjectStore()
	handler := &Handler{
		hocrService:   &failingHOCRProcessor{err: processingErr},
		uploadObjects: objects,
	}

	_, err := handler.ProcessImageURLWithContext(context.Background(), upstream.URL+"/hostile.html", hocr.ProcessingContext{})
	if !errors.Is(err, processingErr) {
		t.Fatalf("ProcessImageURLWithContext error = %v, want processing failure", err)
	}
	imageURL, storedBytes, ok := StoredUploadDetails(err)
	if !ok {
		t.Fatalf("processing failure did not retain cleanup identity: %v", err)
	}
	if storedBytes != uint64(len(payload)) {
		t.Fatalf("processing failure stored bytes = %d, want %d", storedBytes, len(payload))
	}
	name, ok := uploadNameForTest(imageURL)
	if !ok {
		t.Fatalf("cleanup URL = %q", imageURL)
	}
	if got := objects.contentType(name); got != "image/png" {
		t.Fatalf("stored Content-Type = %q, want byte-derived image/png", got)
	}
	if filepath.Ext(name) != ".png" {
		t.Fatalf("stored extension = %q, want .png", filepath.Ext(name))
	}
}

func withUploadWorkingDirectory(t *testing.T) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	temporary := t.TempDir()
	if err := os.Chdir(temporary); err != nil {
		t.Fatalf("enter temporary directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var payload bytes.Buffer
	if err := png.Encode(&payload, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return payload.Bytes()
}

func uploadNameForTest(imageURL string) (string, bool) {
	const prefix = "/static/uploads/"
	if len(imageURL) <= len(prefix) || imageURL[:len(prefix)] != prefix {
		return "", false
	}
	return imageURL[len(prefix):], true
}

func assertNoUploadTemps(t *testing.T) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("uploads", ".scribe-upload-*"))
	if err != nil {
		t.Fatalf("list temporary uploads: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary uploads remain: %v", matches)
	}
}
