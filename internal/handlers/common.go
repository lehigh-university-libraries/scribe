package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/uploadblob"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
)

type Handler struct {
	hocrService   hocrProcessor
	uploadObjects uploadObjectStore
	uploadStager  func(context.Context, string, uint64) error
}

func New() *Handler {
	return &Handler{
		hocrService:   hocr.NewService(),
		uploadObjects: packageUploadObjectStore{},
	}
}

type hocrProcessor interface {
	SetProviderCallAuditLogger(hocr.ProviderCallAuditLogger)
	TranscribeImageWithContext(context.Context, string, string, string) (string, error)
	ProcessImageWithContext(context.Context, string, hocr.ProcessingContext) (string, string, string, error)
}

type uploadObjectStore interface {
	Put(context.Context, string, []byte, string) error
	Delete(context.Context, string) error
}

type packageUploadObjectStore struct{}

func (packageUploadObjectStore) Put(ctx context.Context, name string, data []byte, contentType string) error {
	return uploadblob.Put(ctx, name, data, contentType)
}

func (packageUploadObjectStore) Delete(ctx context.Context, name string) error {
	return uploadblob.Delete(ctx, name)
}

// StoredUploadError identifies a private upload that was created before an
// ingest operation failed. API boundaries use ImageURL to durably schedule a
// retry when the immediate storage compensation is unsuccessful or ambiguous.
type StoredUploadError struct {
	ImageURL    string
	StoredBytes uint64
	Err         error
}

func (e *StoredUploadError) Error() string {
	if e == nil || e.Err == nil {
		return "stored upload processing failed"
	}
	return e.Err.Error()
}

func (e *StoredUploadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StoredUploadURL returns the private upload that needs compensating cleanup.
func StoredUploadURL(err error) (string, bool) {
	imageURL, _, ok := StoredUploadDetails(err)
	return imageURL, ok
}

// StoredUploadDetails returns the private upload and exact physical size that
// must remain quota-accounted until compensating cleanup succeeds.
func StoredUploadDetails(err error) (string, uint64, bool) {
	var uploadErr *StoredUploadError
	if !errors.As(err, &uploadErr) || uploadErr == nil {
		return "", 0, false
	}
	imageURL := strings.TrimSpace(uploadErr.ImageURL)
	if _, ok := uploadref.ImmutableNameFromURL(imageURL); !ok {
		return "", 0, false
	}
	return imageURL, uploadErr.StoredBytes, true
}

func (h *Handler) SetProviderCallAuditLogger(logger hocr.ProviderCallAuditLogger) {
	if h == nil || h.hocrService == nil {
		return
	}
	h.hocrService.SetProviderCallAuditLogger(logger)
}

// SetUploadStager installs the durable pre-write boundary used by the API.
// The callback must commit an immutable cleanup identity before returning.
func (h *Handler) SetUploadStager(stager func(context.Context, string, uint64) error) {
	if h == nil {
		return
	}
	h.uploadStager = stager
}

// File operation helpers
func (h *Handler) ensureUploadsDir() error {
	uploadsDir := "uploads"
	return os.MkdirAll(uploadsDir, 0o750)
}

func (h *Handler) saveUploadedImageBytes(ctx context.Context, imageFilename string, imageData []byte, contentType string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	safeName, err := safeUploadFilename(imageFilename)
	if err != nil {
		return "", err
	}
	if !uploadref.IsImmutableName(safeName) {
		return "", fmt.Errorf("upload filename must use the canonical immutable identity")
	}
	imageURL := "/static/uploads/" + safeName
	if h.uploadStager != nil {
		if err := h.uploadStager(ctx, imageURL, uint64(len(imageData))); err != nil {
			return "", fmt.Errorf("stage immutable upload: %w", err)
		}
	}
	imageFilePath := filepath.Join("uploads", safeName)
	if err := writeUploadAtomically(imageFilePath, imageData); err != nil {
		cleanupCtx, cancel := uploadCleanupContext(ctx)
		cleanupErr := h.deleteUploadedImage(cleanupCtx, safeName)
		cancel()
		return "", &StoredUploadError{
			ImageURL:    imageURL,
			StoredBytes: uint64(len(imageData)),
			Err:         errors.Join(fmt.Errorf("save image: %w", err), cleanupErr),
		}
	}
	if err := h.uploadObjectStore().Put(ctx, safeName, imageData, contentType); err != nil {
		cleanupCtx, cancel := uploadCleanupContext(ctx)
		cleanupErr := h.deleteUploadedImage(cleanupCtx, safeName)
		cancel()
		return "", &StoredUploadError{
			ImageURL:    imageURL,
			StoredBytes: uint64(len(imageData)),
			Err:         errors.Join(fmt.Errorf("save image to shared upload store: %w", err), cleanupErr),
		}
	}
	return imageFilePath, nil
}

func writeUploadAtomically(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".scribe-upload-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Link makes the immutable identity create-only. Unlike Rename, it cannot
	// silently replace an existing canonical object if an invariant regresses.
	if err := os.Link(temporaryPath, path); err != nil { // #nosec G703 -- callers validate the destination basename.
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory) // #nosec G304 -- directory is the validated upload parent.
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return err
	}
	return nil
}

func removeLocalUploadAfterSharedCommit(path string) error {
	if !uploadblob.Enabled() {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) { // #nosec G703 -- path was built from a validated immutable name.
		return fmt.Errorf("remove request-local upload after shared commit: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path)) // #nosec G304 -- path was built from a validated immutable name.
	if err != nil {
		return fmt.Errorf("open upload directory after local removal: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync upload directory after local removal: %w", err)
	}
	return nil
}

func (h *Handler) uploadObjectStore() uploadObjectStore {
	if h != nil && h.uploadObjects != nil {
		return h.uploadObjects
	}
	return packageUploadObjectStore{}
}

func (h *Handler) deleteUploadedImage(ctx context.Context, name string) error {
	validatedName, err := safeUploadFilename(name)
	if err != nil {
		return err
	}
	var cleanupErrors []error
	if err := h.uploadObjectStore().Delete(ctx, validatedName); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("delete shared upload: %w", err))
	}
	if err := os.Remove(filepath.Join("uploads", validatedName)); err != nil && !errors.Is(err, os.ErrNotExist) { // #nosec G703 -- validatedName is a single basename.
		cleanupErrors = append(cleanupErrors, fmt.Errorf("delete local upload: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

func uploadCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
}

func safeUploadFilename(name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" || filepath.Base(clean) != clean || strings.ContainsAny(clean, `/\`) {
		return "", fmt.Errorf("invalid upload filename")
	}
	return clean, nil
}

func (h *Handler) TranscribeImageFileWithContext(ctx context.Context, imagePath, provider, model string) (string, error) {
	return h.hocrService.TranscribeImageWithContext(ctx, imagePath, provider, model)
}

// ProcessImageURLWithContext downloads an image, runs the full segmentation+transcription
// pipeline defined by pctx, and returns a ProcessResult with complete hOCR.
func (h *Handler) ProcessImageURLWithContext(ctx context.Context, imageURL string, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	imageData, err := h.downloadImageFromURL(ctx, imageURL)
	if err != nil {
		return nil, err
	}
	return h.processDataWithContext(ctx, imageData, pctx)
}

// ProcessImageURLTransientWithContext processes an existing item image without
// copying it into the durable upload store. Reprocessing never changes the
// item's painting resource, so persisting a second immutable upload
// would create an object whose lifecycle is not owned by item_images.
func (h *Handler) ProcessImageURLTransientWithContext(ctx context.Context, imageURL string, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	imageData, err := h.loadExistingImage(ctx, imageURL)
	if err != nil {
		return nil, err
	}
	if err := validateUploadedImageData(imageData); err != nil {
		return nil, err
	}
	contentType, ext, err := canonicalImageMediaType(imageData)
	if err != nil {
		return nil, err
	}
	if contentType == "image/jp2" || contentType == "image/tiff" {
		imageData, err = h.convertImageViaHoudini(ctx, imageData, contentType)
		if err != nil {
			return nil, fmt.Errorf("convert image: %w", err)
		}
		if err := validateUploadedImageData(imageData); err != nil {
			return nil, err
		}
		_, ext, err = canonicalImageMediaType(imageData)
		if err != nil {
			return nil, err
		}
	}

	temporary, err := os.CreateTemp("", "scribe-reprocess-*"+ext)
	if err != nil {
		return nil, fmt.Errorf("create transient image: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(imageData); err != nil {
		_ = temporary.Close()
		return nil, fmt.Errorf("write transient image: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, fmt.Errorf("close transient image: %w", err)
	}

	sessionID := "processing_" + uuid.NewString()
	ctx = hocr.WithProviderCallMetadata(ctx, 0, sessionID, nil, nil)
	hocrXML, provider, model, err := h.hocrService.ProcessImageWithContext(ctx, temporaryPath, pctx)
	if err != nil {
		return nil, fmt.Errorf("process image with context: %w", err)
	}
	parsedHOCR, err := hocr.ParseDocument(hocrXML)
	if err != nil {
		return nil, fmt.Errorf("hocr to plain text: %w", err)
	}
	plainText := hocr.PlainText(parsedHOCR.Lines)
	return &ProcessResult{
		SessionID:  sessionID,
		HOCR:       hocrXML,
		PlainText:  plainText,
		ImageURL:   strings.TrimSpace(imageURL),
		Provider:   provider,
		Model:      model,
		ParsedHOCR: &parsedHOCR,
	}, nil
}

func (h *Handler) loadExistingImage(ctx context.Context, imageURL string) ([]byte, error) {
	if name, ok := uploadref.ImmutableNameFromURL(imageURL); ok {
		if uploadblob.Enabled() {
			data, _, err := uploadblob.Read(ctx, name)
			if err != nil {
				return nil, fmt.Errorf("read stored image: %w", err)
			}
			return data, nil
		}
		path := filepath.Join("uploads", name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat stored image: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxUploadedImageBytes {
			return nil, invalidImageErrorf("stored image exceeds processing limits")
		}
		data, err := os.ReadFile(path) // #nosec G304 -- name is a validated single upload-object basename.
		if err != nil {
			return nil, fmt.Errorf("read stored image: %w", err)
		}
		return data, nil
	}
	if _, localButNonCanonical := uploadref.NameFromURL(imageURL); localButNonCanonical {
		return nil, invalidImageErrorf("stored image identity is not immutable")
	}
	return h.downloadImageFromURL(ctx, imageURL)
}

// ProcessImageUploadWithContext saves uploaded image bytes, runs the full
// segmentation+transcription pipeline defined by pctx, and returns a ProcessResult
// with complete hOCR.
func (h *Handler) ProcessImageUploadWithContext(ctx context.Context, filename string, fileData []byte, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	return h.processDataWithContext(ctx, fileData, pctx)
}

// processDataWithContext is the shared implementation for ProcessImageURLWithContext
// and ProcessImageUploadWithContext.
func (h *Handler) processDataWithContext(ctx context.Context, imageData []byte, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	if err := validateUploadedImageData(imageData); err != nil {
		return nil, err
	}

	contentType, ext, err := canonicalImageMediaType(imageData)
	if err != nil {
		return nil, err
	}
	if contentType == "image/jp2" || contentType == "image/tiff" {
		converted, err := h.convertImageViaHoudini(ctx, imageData, contentType)
		if err != nil {
			return nil, fmt.Errorf("convert image: %w", err)
		}
		imageData = converted
		if err := validateUploadedImageData(imageData); err != nil {
			return nil, err
		}
		contentType, ext, err = canonicalImageMediaType(imageData)
		if err != nil {
			return nil, err
		}
	}

	imageFilename := immutableUploadName(imageData, ext)
	imageFilePath, err := h.saveUploadedImageBytes(ctx, imageFilename, imageData, contentType)
	if err != nil {
		return nil, err
	}

	imageLocalURL := "/static/uploads/" + imageFilename
	cleanupOnFailure := func(processingErr error) error {
		cleanupCtx, cancel := uploadCleanupContext(ctx)
		cleanupErr := h.deleteUploadedImage(cleanupCtx, imageFilename)
		cancel()
		return &StoredUploadError{ImageURL: imageLocalURL, StoredBytes: uint64(len(imageData)), Err: errors.Join(processingErr, cleanupErr)}
	}
	sessionID := "processing_" + uuid.NewString()
	ctx = hocr.WithProviderCallMetadata(ctx, 0, sessionID, nil, nil)

	hocrXML, provider, model, err := h.hocrService.ProcessImageWithContext(ctx, imageFilePath, pctx)
	if err != nil {
		return nil, cleanupOnFailure(fmt.Errorf("process image with context: %w", err))
	}

	parsedHOCR, err := hocr.ParseDocument(hocrXML)
	if err != nil {
		return nil, cleanupOnFailure(fmt.Errorf("hocr to plain text: %w", err))
	}
	plainText := hocr.PlainText(parsedHOCR.Lines)
	if err := removeLocalUploadAfterSharedCommit(imageFilePath); err != nil {
		return nil, cleanupOnFailure(err)
	}

	return &ProcessResult{
		SessionID:   sessionID,
		HOCR:        hocrXML,
		PlainText:   plainText,
		ImageURL:    imageLocalURL,
		StoredBytes: uint64(len(imageData)), // #nosec G115 -- slice length is non-negative and fits uint64.
		Provider:    provider,
		Model:       model,
		ParsedHOCR:  &parsedHOCR,
	}, nil
}
