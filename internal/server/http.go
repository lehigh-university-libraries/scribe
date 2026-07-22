package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/uploadblob"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
	"github.com/lehigh-university-libraries/scribe/internal/vaultkv"
	"github.com/lehigh-university-libraries/scribe/internal/worklimit"
)

type Handler struct {
	ocrRuns                     *store.OCRRunStore
	items                       *store.ItemStore
	contexts                    *store.ContextStore
	annotations                 *store.AnnotationStore
	providerCallAudits          *store.ProviderCallAuditStore
	providerSecrets             providerSecretResolver
	transcriptionJobs           *store.TranscriptionJobStore
	transcriptionQueue          TranscriptionJobQueue
	auth                        *auth.Manager
	appCtx                      context.Context
	vault                       providerSecretVault
	webhookURLs                 []string
	mux                         http.Handler
	ocr                         OCRProcessor
	requestLimiter              *requestLimiter
	edgeRequestLimiter          *requestLimiter
	edgeAggregateLimiter        *requestLimiter
	largeBodyLimiter            *bodyConcurrencyLimiter
	canonicalReadLimiter        *bodyConcurrencyLimiter
	readinessLimiter            *bodyConcurrencyLimiter
	sseLimiter                  *connectionLimiter
	processingLimiter           *worklimit.HierarchicalLimiter
	maxManifestCanvases         int
	maxManifestImportBytes      uint64
	manifestImportTimeout       time.Duration
	itemPageTokens              *itemPageTokenCodec
	itemExportTokens            *itemExportTokenCodec
	exportLimiter               *bodyConcurrencyLimiter
	imageRegionFetcher          func(context.Context, string, int, int, int, int) (string, func(), error)
	deleteUploadBlob            func(context.Context, string) error
	deleteTripletImageGraphFn   func(context.Context, uint64) error
	reconcileTripletItemGraphFn func(context.Context, string) error
	transcriptionWorkerWG       sync.WaitGroup
	backgroundWorkerWG          sync.WaitGroup
}

// OCRProcessor is the application boundary around segmentation and
// transcription. Keeping handlers behind this narrow interface makes provider
// behavior replaceable and lets API tests exercise transaction semantics
// without invoking heavyweight model runtimes.
type OCRProcessor interface {
	SetProviderCallAuditLogger(hocr.ProviderCallAuditLogger)
	ProcessImageURLWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error)
	ProcessImageURLTransientWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error)
	ProcessImageUploadWithContext(context.Context, string, []byte, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error)
	StoreUploadedImage(context.Context, string, []byte) (string, error)
	TranscribeImageFileWithContext(context.Context, string, string, string) (string, error)
}

type uploadStagingOCRProcessor interface {
	SetUploadStager(func(context.Context, string, uint64) error)
}

type TranscriptionJobQueue interface {
	PublishTranscriptionJob(context.Context, uint64) error
	ReceiveTranscriptionJobs(context.Context, func(context.Context, uint64) error, func(context.Context, string, error, []byte)) error
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *responseWriter) Flush() {
	flusher, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()
}

// Unwrap lets http.ResponseController reach the network writer for per-export
// deadlines while AccessLogger still records the final status.
func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func AccessLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/healthz" || r.URL.Path == "/livez" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w}

		next.ServeHTTP(wrapped, r)
		if wrapped.statusCode == 0 {
			wrapped.statusCode = http.StatusOK
		}

		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.statusCode,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

func NewHandler(
	ocrRuns *store.OCRRunStore,
	items *store.ItemStore,
	contexts *store.ContextStore,
	annotations *store.AnnotationStore,
	transcriptionJobs *store.TranscriptionJobStore,
	authManager *auth.Manager,
	providerSecrets *store.ProviderSecretStore,
	vaultClient *vaultkv.Client,
	auditStores ...*store.ProviderCallAuditStore,
) *Handler {
	var providerCallAudits *store.ProviderCallAuditStore
	if len(auditStores) > 0 {
		providerCallAudits = auditStores[0]
	}

	maxManifestCanvases := config.Get().Config.IIIF.MaxManifestCanvases
	if maxManifestCanvases == 0 {
		maxManifestCanvases = config.DefaultMaxManifestCanvases
	}
	maxManifestImportBytes := config.Get().Config.IIIF.MaxManifestImportBytes
	if maxManifestImportBytes == 0 {
		maxManifestImportBytes = config.DefaultMaxManifestImportBytes
	}
	var itemPageTokens *itemPageTokenCodec
	var itemExportTokens *itemExportTokenCodec
	if signingKey := config.Get().Config.Pagination.SigningKey; signingKey != "" {
		codec, codecErr := newItemPageTokenCodec(signingKey)
		if codecErr != nil {
			slog.Error("item pagination is disabled because its signing key is invalid", "error_type", safeLogErrorType(codecErr))
		} else {
			itemPageTokens = codec
		}
		exportCodec, exportCodecErr := newItemExportTokenCodec(signingKey)
		if exportCodecErr != nil {
			slog.Error("item exports are disabled because their signing key is invalid", "error_type", safeLogErrorType(exportCodecErr))
		} else {
			itemExportTokens = exportCodec
		}
	}
	handler := &Handler{
		ocrRuns:                ocrRuns,
		items:                  items,
		contexts:               contexts,
		annotations:            annotations,
		providerCallAudits:     providerCallAudits,
		providerSecrets:        providerSecrets,
		transcriptionJobs:      transcriptionJobs,
		auth:                   authManager,
		appCtx:                 context.Background(),
		vault:                  vaultClient,
		webhookURLs:            append([]string(nil), config.Get().Config.Webhooks.URLs...),
		ocr:                    ocrhandlers.New(),
		requestLimiter:         newRequestLimiter(),
		edgeRequestLimiter:     newEdgeRequestLimiter(),
		edgeAggregateLimiter:   newEdgeAggregateLimiter(),
		largeBodyLimiter:       newBodyConcurrencyLimiter(maxConcurrentLargeBodies, maxConcurrentLargeBodiesPerClient),
		canonicalReadLimiter:   newBodyConcurrencyLimiter(maxConcurrentCanonicalReads, maxConcurrentCanonicalReadsPerKey),
		readinessLimiter:       newBodyConcurrencyLimiter(2, 1),
		sseLimiter:             newConnectionLimiter(),
		processingLimiter:      newProcessingLimiter(config.Get().Config.Processing),
		maxManifestCanvases:    maxManifestCanvases,
		maxManifestImportBytes: maxManifestImportBytes,
		manifestImportTimeout:  defaultManifestImportTimeout,
		itemPageTokens:         itemPageTokens,
		itemExportTokens:       itemExportTokens,
		exportLimiter:          newBodyConcurrencyLimiter(maxConcurrentExports, maxConcurrentExportsPerWorkspace),
	}
	if annotations != nil {
		if err := annotations.SetStorageQuotaLimits(configuredStorageQuotaLimits()); err != nil {
			slog.Error("annotation storage quota policy is invalid", "error_type", safeLogErrorType(err))
		}
	}
	if ocrRuns != nil {
		if err := ocrRuns.SetStorageQuotaLimits(configuredStorageQuotaLimits()); err != nil {
			slog.Error("OCR provenance storage quota policy is invalid", "error_type", safeLogErrorType(err))
		}
	}
	if stagedOCR, ok := handler.ocr.(uploadStagingOCRProcessor); ok && items != nil {
		stagedOCR.SetUploadStager(handler.stageImmutableUpload)
	}
	if providerCallAudits != nil {
		if err := providerCallAudits.SetStorageQuotaLimits(configuredStorageQuotaLimits()); err != nil {
			slog.Error("provider call audit storage quota policy is invalid", "error_type", safeLogErrorType(err))
		}
		handler.ocr.SetProviderCallAuditLogger(func(ctx context.Context, record hocr.ProviderCallAuditRecord) {
			if err := providerCallAudits.Create(ctx, store.ProviderCallAudit{
				WorkspaceID:  record.WorkspaceID,
				SessionID:    record.SessionID,
				ItemImageID:  record.ItemImageID,
				ContextID:    record.ContextID,
				Provider:     record.Provider,
				Model:        record.Model,
				Operation:    record.Operation,
				ErrorMessage: record.ErrorMessage,
				HTTPStatus:   record.HTTPStatus,
				DurationMS:   record.DurationMS,
			}); err != nil {
				slog.Warn("failed to persist provider call audit", "error_type", safeLogErrorType(err), "provider", record.Provider, "model", record.Model, "operation", record.Operation)
			}
		})
	}
	mux := http.NewServeMux()
	registerConnectServices(mux, handler, authManager, connectHandlerOptions(authManager)...)

	// Liveness only checks the process. Readiness verifies required persistence.
	mux.HandleFunc("GET /livez", handler.handleLiveness)
	mux.HandleFunc("GET /readyz", handler.handleReadiness)
	mux.HandleFunc("GET /healthz", handler.handleReadiness)

	mux.HandleFunc("GET /v1/item-images/{item_image_id}/annotations/revisions/{revision}/hocr", handler.handleGetHOCR)
	mux.HandleFunc("HEAD /v1/item-images/{item_image_id}/annotations/revisions/{revision}/hocr", handler.handleGetHOCR)
	mux.HandleFunc("GET /v1/item-exports/{token}", handler.handlePreparedItemExport)
	mux.HandleFunc("HEAD /v1/item-exports/{token}", handler.handlePreparedItemExport)
	mux.HandleFunc("GET /v1/events", handler.handleEventStream)

	if authManager != nil {
		authManager.RegisterRoutes(mux)
	}

	// Triplet dereferences these immutable source bytes to serve Image API
	// resources. Scribe owns the constrained internal-source credential plus
	// normal workspace and explicit-publication authorization.
	mux.HandleFunc("GET /static/uploads/", handler.handleStaticUpload)
	mux.HandleFunc("HEAD /static/uploads/", handler.handleStaticUpload)
	// The dedicated frontend image is the only application-shell server. Keep
	// the API fail-closed so an accidentally copied web/dist cannot create a
	// second delivery path with different proxy and browser-security behavior.
	mux.HandleFunc("/", http.NotFound)
	// Edge admission runs before credential verification so invalid API keys or
	// JWTs cannot bypass rate, size, compression, or large-body concurrency
	// limits. The inner limiter sees the authenticated principal and enforces
	// the workspace/user rate bucket before dispatch.
	finalMux := handler.requestLimitMiddleware(mux)
	if authManager != nil {
		finalMux = authManager.Middleware(finalMux)
	}
	finalMux = handler.requestAdmissionMiddleware(finalMux)
	handler.mux = AccessLogger(finalMux)
	return handler
}

func (h *Handler) SetAppContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	h.appCtx = ctx
}

func (h *Handler) backgroundContext() context.Context {
	if h != nil && h.appCtx != nil {
		return h.appCtx
	}
	return context.Background()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !applyCORS(w, r) {
		return
	}
	h.mux.ServeHTTP(w, r)
}

func applyCORS(w http.ResponseWriter, r *http.Request) bool {
	if isPublicUploadReadRequest(r) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept,If-None-Match,If-Modified-Since,Range")
		w.Header().Set("Access-Control-Expose-Headers", "ETag,Last-Modified,Content-Length,Content-Range,Accept-Ranges")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return false
		}
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return false
		}
		return true
	}

	addVaryHeader(w.Header(), "Origin")
	if !corsOriginAllowed(r, origin) {
		writeError(w, http.StatusForbidden, "origin is not allowed")
		return false
	}

	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,POST,PUT,DELETE,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Accept,Authorization,Connect-Protocol-Version,X-Scribe-Workspace-ID,X-Scribe-API-Key")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	return true
}

func isPublicUploadReadRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		return false
	}
	return auth.IsPublicUploadSourceRequest(r.URL.Path, http.MethodGet)
}

func addVaryHeader(header http.Header, value string) {
	existing := header.Values("Vary")
	for _, entry := range existing {
		for _, part := range strings.Split(entry, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

func corsOriginAllowed(r *http.Request, rawOrigin string) bool {
	origin, ok := canonicalOrigin(rawOrigin)
	if !ok {
		return false
	}
	for _, candidate := range allowedCORSOrigins(r) {
		if candidate == origin {
			return true
		}
	}
	return false
}

func allowedCORSOrigins(r *http.Request) []string {
	cfg := config.Get().Config
	rawOrigins := make([]string, 0, 2+len(cfg.CORS.AllowedOrigins))
	rawOrigins = append(rawOrigins, cfg.PublicBaseURL, requestCORSOrigin(r))
	rawOrigins = append(rawOrigins, cfg.CORS.AllowedOrigins...)

	seen := make(map[string]struct{}, len(rawOrigins))
	origins := make([]string, 0, len(rawOrigins))
	for _, raw := range rawOrigins {
		origin, ok := canonicalOrigin(raw)
		if !ok {
			continue
		}
		if _, exists := seen[origin]; exists {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins
}

func requestCORSOrigin(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Host)
	if isTrustedProxy(r.RemoteAddr) {
		forwarded := forwardedParams(r.Header.Get("Forwarded"))
		if forwardedProto := lastForwardedValue(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
			scheme = forwardedProto
		}
		if forwarded["proto"] != "" {
			scheme = forwarded["proto"]
		}
		if forwardedHost := lastForwardedValue(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
			host = forwardedHost
		} else if forwarded["host"] != "" {
			host = forwarded["host"]
		}
	}
	origin, _ := canonicalOrigin(scheme + "://" + host)
	return origin
}

func canonicalOrigin(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return "", false
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	return scheme + "://" + strings.ToLower(strings.TrimSpace(u.Host)), true
}

func (h *Handler) SetTranscriptionJobQueue(q TranscriptionJobQueue) {
	h.transcriptionQueue = q
}

func (h *Handler) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if h.items == nil || h.items.Ping(ctx) != nil {
		writeJSON(w, http.StatusServiceUnavailable, readinessResponse("not_ready"))
		return
	}
	writeJSON(w, http.StatusOK, readinessResponse("ready"))
}

func readinessResponse(status string) map[string]string {
	response := map[string]string{"status": status}
	if image := strings.TrimSpace(os.Getenv("SCRIBE_DEPLOYED_API_IMAGE")); image != "" {
		response["api_image"] = image
	}
	return response
}

func (h *Handler) handleStaticUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	setPrivateUploadSourceHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	name, err := uploadNameFromRequestPath(r.URL.Path)
	if err != nil || r.URL.RawQuery != "" || !uploadref.IsImmutableName(name) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	imageURL := staticUploadsPrefix + name
	ownerAccess, publishedAccess, err := h.authorizeUploadSource(r.Context(), imageURL)
	if err != nil {
		slog.Error("authorize upload source", "error_type", safeLogErrorType(err))
		writeError(w, http.StatusInternalServerError, "failed to authorize upload")
		return
	}
	if !ownerAccess && !publishedAccess {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if publishedAccess && !ownerAccess {
		setPublicUploadSourceHeaders(w)
	}
	if uploadblob.Enabled() {
		data, attrs, err := uploadblob.Read(r.Context(), name)
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		contentType, ok := storedUploadMediaType(data)
		if !ok {
			writeError(w, http.StatusUnsupportedMediaType, "stored upload is not a supported image")
			return
		}
		w.Header().Set("Content-Type", contentType)
		http.ServeContent(w, r, name, attrs.Updated, bytes.NewReader(data))
		return
	}
	f, err := safefile.Open(filepath.Join("uploads", name))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	prefix := make([]byte, 512)
	n, readErr := f.Read(prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		writeError(w, http.StatusInternalServerError, "failed to read upload")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read upload")
		return
	}
	contentType, ok := storedUploadMediaType(prefix[:n])
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "stored upload is not a supported image")
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (h *Handler) authorizeUploadSource(ctx context.Context, imageURL string) (ownerAccess, publishedAccess bool, err error) {
	principal, hasPrincipal := auth.PrincipalFromContext(ctx)
	if hasPrincipal && principal.CanReadRawSource() {
		return true, false, nil
	}
	if hasPrincipal && principal.HasPermission("annotations:read") && h.items != nil {
		if strings.EqualFold(strings.TrimSpace(principal.AuthType), "session") && principal.UserID > 0 {
			ownerAccess, err = h.items.UserCanReadImageURL(ctx, principal.UserID, imageURL)
		} else if principal.WorkspaceID > 0 {
			// API keys, external JWTs, and any future delegated credential stay
			// within the workspace selected when the principal was created.
			ownerAccess, err = h.items.WorkspaceOwnsImageURL(ctx, principal.WorkspaceID, imageURL)
		}
		if err != nil || ownerAccess {
			return ownerAccess, false, err
		}
	}
	if h.annotations == nil {
		return false, false, nil
	}
	publishedAccess, err = h.annotations.ImageURLIsPublished(ctx, imageURL)
	return false, publishedAccess, err
}

func setPrivateUploadSourceHeaders(w http.ResponseWriter) {
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Del("Access-Control-Allow-Origin")
	w.Header().Del("Access-Control-Allow-Credentials")
	w.Header().Del("Access-Control-Allow-Methods")
	w.Header().Del("Access-Control-Allow-Headers")
	w.Header().Del("Access-Control-Expose-Headers")
	for _, name := range []string{"Authorization", "Cookie", "X-Scribe-API-Key", "X-Scribe-Workspace-ID"} {
		addVaryHeader(w.Header(), name)
	}
}

func setPublicUploadSourceHeaders(w http.ResponseWriter) {
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept,If-None-Match,If-Modified-Since,Range")
	w.Header().Set("Access-Control-Expose-Headers", "ETag,Last-Modified,Content-Length,Content-Range,Accept-Ranges")
	w.Header().Del("Access-Control-Allow-Credentials")
}

func storedUploadMediaType(data []byte) (string, bool) {
	mediaType, err := ocrhandlers.UploadedImageMediaType(data)
	return mediaType, err == nil
}

func uploadNameFromRequestPath(requestPath string) (string, error) {
	rawName := strings.TrimPrefix(strings.TrimSpace(requestPath), staticUploadsPrefix)
	name, err := url.PathUnescape(rawName)
	if err != nil {
		return "", err
	}
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid upload name")
	}
	return name, nil
}

func (h *Handler) handleGetHOCR(w http.ResponseWriter, r *http.Request) {
	itemImageID, err := itemImageIDFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	revision, err := strconv.ParseUint(strings.TrimSpace(r.PathValue("revision")), 10, 64)
	if err != nil || revision == 0 {
		writeError(w, http.StatusBadRequest, "revision must be a positive integer")
		return
	}
	ctx, finish := exportRequestContext(w, r)
	defer finish()
	release, allowed := h.exportLimiter.TryAcquire(fmt.Sprintf("workspace:%d", h.currentWorkspaceID(ctx)))
	if !allowed {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "export concurrency limit exceeded")
		return
	}
	defer release()
	page, err := h.loadCanonicalExportPage(ctx, itemImageID, revision)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "canonical hocr generation timed out")
		case errors.Is(err, errItemExportRevisionConflict):
			writeError(w, http.StatusConflict, "canonical annotations changed; reload the manifest")
		case errors.Is(err, store.ErrAnnotationPageNotFound):
			writeError(w, http.StatusNotFound, "item image not found")
		default:
			slog.Error("load revisioned hocr source", "item_image_id", itemImageID, "error_type", safeLogErrorType(err))
			writeError(w, http.StatusInternalServerError, "failed to load canonical annotations")
		}
		return
	}
	hocrXML, _, _, err := renderCanonicalExportPage(page, "hocr")
	if err != nil {
		if errors.Is(err, errItemExportOutputLimit) {
			writeError(w, http.StatusRequestEntityTooLarge, "canonical hocr exceeds the output-byte limit")
		} else {
			writeError(w, http.StatusUnprocessableEntity, "canonical hocr could not be generated")
		}
		return
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "canonical hocr generation timed out")
		}
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="item-%d.hocr"`, itemImageID))
	w.Header().Set("Content-Type", "text/vnd.hocr+html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(hocrXML)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	// #nosec G705 -- ConvertHOCRLinesToXML escapes canonical annotation text and IDs;
	// attachment disposition plus nosniff prevents the HTML-compatible format from rendering inline.
	if _, err := copyExportContent(ctx, w, strings.NewReader(hocrXML)); err != nil {
		slog.Warn("stream revisioned hocr failed", "item_image_id", itemImageID, "revision", revision, "error_type", fmt.Sprintf("%T", err))
	}
}

func sanitizeFilenamePart(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return fallback
	}
	return out
}

func joinLineWords(line models.HOCRLine) string {
	parts := make([]string, 0, len(line.Words))
	for _, word := range line.Words {
		txt := strings.TrimSpace(word.Text)
		if txt == "" {
			continue
		}
		parts = append(parts, txt)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func lineAnnotationText(line models.HOCRLine) string {
	return joinLineWords(line)
}

func buildLineAnnotations(sessionID, canvasID string, lines []models.HOCRLine) []any {
	items := make([]any, 0, len(lines))
	for i, line := range lines {
		width := line.BBox.X2 - line.BBox.X1
		height := line.BBox.Y2 - line.BBox.Y1
		if width <= 0 || height <= 0 {
			continue
		}
		text := strings.TrimSpace(lineAnnotationText(line))
		lineID := strings.TrimSpace(line.ID)
		if lineID == "" {
			lineID = fmt.Sprintf("line-%d", i+1)
		}
		annID := annotationID(sessionID, "line", lineID)
		items = append(items, transcriptionAnnotation(annID, "line", text, canvasID, line.BBox))
	}
	return items
}

func buildWordAnnotations(sessionID, canvasID string, words []models.HOCRWord) []any {
	items := make([]any, 0, len(words))
	for i, word := range words {
		width := word.BBox.X2 - word.BBox.X1
		height := word.BBox.Y2 - word.BBox.Y1
		if width <= 0 || height <= 0 {
			continue
		}
		wordID := strings.TrimSpace(word.ID)
		if wordID == "" {
			wordID = fmt.Sprintf("word-%d", i+1)
		}
		annID := annotationID(sessionID, "word", wordID)
		items = append(items, transcriptionAnnotation(annID, "word", strings.TrimSpace(word.Text), canvasID, word.BBox))
	}
	return items
}

func transcriptionAnnotation(id, granularity, text, canvasID string, box models.BBox) map[string]any {
	width := box.X2 - box.X1
	height := box.Y2 - box.Y1
	anno := map[string]any{
		"id":              id,
		"type":            "Annotation",
		"textGranularity": granularity,
		"motivation":      "supplementing",
		"body": []any{
			map[string]any{
				"type":    "TextualBody",
				"purpose": "supplementing",
				"format":  "text/plain",
				"value":   text,
			},
		},
	}
	anno["target"] = map[string]any{
		"source": map[string]any{
			"id":   canvasID,
			"type": "Canvas",
		},
		"selector": map[string]any{
			"type":       "FragmentSelector",
			"conformsTo": "http://www.w3.org/TR/media-frags/",
			"value":      fmt.Sprintf("xywh=%d,%d,%d,%d", box.X1, box.Y1, width, height),
		},
	}
	return anno
}

func itemImageIDFromRequest(r *http.Request) (uint64, error) {
	itemImageIDRaw := strings.TrimSpace(r.PathValue("item_image_id"))
	if itemImageIDRaw == "" {
		return 0, fmt.Errorf("item_image_id is required")
	}
	itemImageID, err := strconv.ParseUint(itemImageIDRaw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid item_image_id")
	}
	return itemImageID, nil
}

func (h *Handler) internalAnnotationBaseURL() string {
	base := strings.TrimRight(strings.TrimSpace(config.Get().Config.Annotation.APIInternalBase), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(config.Get().Config.Annotation.APIBase), "/")
	}
	if base == "" {
		base = "http://localhost:8080"
	}
	return base
}

func (h *Handler) ensureItemImageCanvasAndAnnotations(ctx context.Context, run store.OCRRun, itemImageID uint64, parsed *hocr.Document) error {
	img, err := h.itemImageForRequest(ctx, itemImageID)
	if err != nil {
		return fmt.Errorf("get item image: %w", err)
	}

	canvasURI := strings.TrimSpace(img.CanvasURI)
	if canvasURI == "" {
		return fmt.Errorf("item image %d has no Canvas identity", itemImageID)
	}

	if _, err := h.annotations.LoadPage(ctx, h.currentWorkspaceID(ctx), itemImageID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrAnnotationPageNotFound) {
		return fmt.Errorf("load annotation page: %w", err)
	}

	annotationScopeID := run.SessionID
	if run.ItemImageID != nil {
		annotationScopeID = fmt.Sprintf("item-image-%d", *run.ItemImageID)
	}

	items := make([]any, 0)
	hocrXML := strings.TrimSpace(run.OriginalHOCR)
	if hocrXML != "" {
		document, parseErr := parsedHOCRDocument(hocrXML, parsed)
		if parseErr != nil {
			return fmt.Errorf("parse hocr: %w", parseErr)
		}
		items = append(items, buildLineAnnotations(annotationScopeID, canvasURI, document.Lines)...)
		items = append(items, buildWordAnnotations(annotationScopeID, canvasURI, document.Words)...)
	}
	created, err := h.createInitialAnnotationPage(ctx, img, items)
	if err != nil {
		return fmt.Errorf("persist annotations: %w", err)
	}
	if !created {
		return nil
	}
	pageID, _ := h.annotationPageIDForItemImage(itemImageID)
	h.publishEvent("dev.scribe.annotations.created", subjectForItemImage(itemImageID), map[string]any{
		"itemImageId":      itemImageID,
		"canvasUri":        canvasURI,
		"annotationCount":  len(items),
		"annotationPageId": pageID,
	})
	return nil
}

// createInitialAnnotationPage performs an atomic revision-zero create. A
// concurrent importer/editor that wins the race owns the canonical page; the
// initializer must never reload that newer revision and replace its contents.
func (h *Handler) createInitialAnnotationPage(ctx context.Context, image store.ItemImage, items []any) (bool, error) {
	canvasURI := strings.TrimSpace(image.CanvasURI)
	if canvasURI == "" {
		return false, fmt.Errorf("item image %d has no Canvas identity", image.ID)
	}
	pageID, err := h.annotationPageIDForItemImage(image.ID)
	if err != nil {
		return false, err
	}
	payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: h.publicAnnotationBaseURL(),
		ItemImageID:   image.ID,
		CanvasURI:     canvasURI,
	}, items)
	if err != nil {
		return false, err
	}
	userID := h.currentUserID(ctx)
	page := store.AnnotationPage{
		WorkspaceID: h.currentWorkspaceID(ctx),
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(payload),
	}
	if userID > 0 {
		page.UpdatedByUserID = &userID
	}
	if _, err := h.annotations.SavePage(ctx, page, 0); err != nil {
		if errors.Is(err, store.ErrAnnotationRevisionConflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// buildImageBody returns a IIIF Presentation v3 painting body for the given image URL.
// For local uploads it wraps the image in a IIIF Image Service descriptor.
// For external URLs it detects and reuses any IIIF image service embedded in the URL;
// otherwise it returns a plain Image body.
func buildImageBody(imageURL, sourceBase, iiifBase string, pageW, pageH int) map[string]any {
	body := map[string]any{
		"type":   "Image",
		"height": pageH,
		"width":  pageW,
	}

	// Local upload: expose Triplet's IIIF Image API service.
	if strings.HasPrefix(imageURL, "/static/uploads/") {
		iiifID, err := iiifIdentifierFromImageURL(imageURL, sourceBase)
		if err == nil {
			serviceID := iiifBase + "/" + iiifID
			body["id"] = serviceID + "/full/max/0/default.jpg"
			body["format"] = "image/jpeg"
			body["service"] = []any{iiifServiceDescriptor(serviceID, true)}
			return body
		}
	}

	// External URL: use as-is and try to attach a IIIF service descriptor.
	body["id"] = imageURL
	if format := imageContentTypeFromURL(imageURL); format != "" {
		body["format"] = format
	}
	if serviceID := iiifServiceFromImageURL(imageURL); serviceID != "" {
		body["service"] = []any{iiifServiceDescriptor(serviceID, false)}
	}
	return body
}

func imageContentTypeFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	switch strings.ToLower(path.Ext(parsed.Path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".jp2", ".j2k", ".jpx":
		return "image/jp2"
	default:
		return ""
	}
}

func itemImageDimensions(image store.ItemImage) (int, int) {
	return maxInt(1, int(image.Width)), maxInt(1, int(image.Height))
}

func iiifServiceDescriptor(serviceID string, scribeService bool) map[string]any {
	if strings.Contains(serviceID, "/iiif/3/") {
		profile := iiif.ImageProfileLevel0V3
		if scribeService {
			profile = iiif.ImageProfileLevel2V3
		}
		return map[string]any{
			"id":      serviceID,
			"type":    iiif.ImageServiceTypeV3,
			"profile": string(profile),
		}
	}
	profile := "http://iiif.io/api/image/2/level0.json"
	if scribeService {
		profile = "http://iiif.io/api/image/2/level1.json"
	}
	return map[string]any{
		"id":      serviceID,
		"type":    "ImageService2",
		"profile": profile,
	}
}

// iiifServiceFromImageURL extracts the IIIF image service base URL from a full
// IIIF image URL by stripping the trailing region/size/rotation/quality segments.
// Returns "" if the URL does not appear to be a IIIF image URL.
func iiifServiceFromImageURL(imageURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(imageURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	escapedPath := parsed.EscapedPath()
	for _, seg := range []string{"/iiif/2/", "/iiif/3/"} {
		marker := strings.Index(escapedPath, seg)
		if marker < 0 {
			continue
		}
		// Strip the last 4 path segments (region/size/rotation/quality.format).
		servicePath := escapedPath
		for i := 0; i < 4; i++ {
			idx := strings.LastIndex(servicePath, "/")
			if idx < marker+len(seg) {
				return ""
			}
			servicePath = servicePath[:idx]
		}
		if servicePath == escapedPath[:marker+len(seg)] {
			return ""
		}
		decodedPath, err := url.PathUnescape(servicePath)
		if err != nil {
			return ""
		}
		parsed.Path = decodedPath
		parsed.RawPath = servicePath
		return parsed.String()
	}
	return ""
}

func iiifIdentifierFromImageURL(imageURL, sourceBase string) (string, error) {
	name, ok := uploadNameFromURL(imageURL)
	if !ok {
		return "", fmt.Errorf("image is not an immutable application upload")
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(sourceBase), "/"))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", fmt.Errorf("source base must be an absolute HTTP(S) URL")
	}
	sourceURL := strings.TrimRight(base.String(), "/") + staticUploadsPrefix + url.PathEscape(name)
	return url.PathEscape(sourceURL), nil
}

func effectiveModel(provider, requestModel string) string {
	model, _ := providerregistry.New(config.Get().Config).EffectiveModel(provider, requestModel)
	return model
}

func effectiveProvider(requestProvider string) string {
	descriptor, _ := providerregistry.New(config.Get().Config).ResolveProvider(requestProvider)
	return descriptor.ID
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func isTrustedProxy(remoteAddr string) bool {
	return isTrustedProxyFrom(remoteAddr, config.Get().Config.Server.TrustedProxyCIDRs)
}

func isTrustedProxyFrom(remoteAddr string, cidrs config.CIDRList) bool {
	return config.AddressInCIDRs(remoteAddr, cidrs)
}

func lastForwardedValue(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	part := strings.TrimSpace(parts[len(parts)-1])
	part = strings.Trim(part, "\"")
	return strings.TrimSpace(part)
}

func forwardedParams(raw string) map[string]string {
	params := map[string]string{}
	entry := lastForwardedValue(raw)
	if entry == "" {
		return params
	}
	for _, part := range strings.Split(entry, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(strings.Trim(value, "\""))
		if key != "" && value != "" {
			params[key] = value
		}
	}
	return params
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

func exportRequestContext(w http.ResponseWriter, r *http.Request) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(r.Context(), maxPreparedExportDuration)
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	deadlineApplied := controller.SetWriteDeadline(deadline.Add(exportWriteDeadlineGrace)) == nil
	return ctx, func() {
		cancel()
		if deadlineApplied {
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}
}

func renderCanonicalExportPage(page canonicalExportPage, format string) (string, string, string, error) {
	content, mimeType, extension, err := renderAnnotationExport(page.Page.Payload, int(page.Image.Width), int(page.Image.Height), format)
	if err != nil {
		slog.Warn("Export annotations crosswalk failed",
			"item_image_id", page.Image.ID,
			"format", format,
			"source", "canonical",
			"error_type", safeLogErrorType(err),
		)
		return "", "", "", err
	}
	slog.Info("Export annotations crosswalk succeeded",
		"item_image_id", page.Image.ID,
		"format", format,
		"source", "canonical",
		"revision", page.Page.Revision,
	)
	return content, mimeType, extension, nil
}

// renderAnnotationExport is the single crosswalk boundary used by Connect and
// browser download adapters. Its only input state is one committed canonical
// AnnotationPage plus the owning Canvas dimensions.
func renderAnnotationExport(annotationPageJSON string, canvasWidth, canvasHeight int, format string) (string, string, string, error) {
	lines, pageW, pageH, err := annotationPageToHOCRLinesWithDimensions(annotationPageJSON, canvasWidth, canvasHeight)
	if err != nil {
		return "", "", "", fmt.Errorf("no annotations available: %w", err)
	}

	var content, mediaType, extension string
	switch format {
	case "hocr":
		converter := hocr.NewConverter()
		content, mediaType, extension = converter.ConvertHOCRLinesToXML(lines, pageW, pageH), "text/vnd.hocr+html; charset=utf-8", "hocr"
	case "pagexml":
		content, mediaType, extension = linesToPageXML(lines, pageW, pageH), "application/vnd.prima.page+xml; charset=utf-8", "xml"
	case "alto":
		content, mediaType, extension = linesToALTOXML(lines, pageW, pageH), "application/alto+xml; charset=utf-8", "xml"
	case "txt":
		content, mediaType, extension = linesToPlainText(lines), "text/plain; charset=utf-8", "txt"
	default:
		return "", "", "", fmt.Errorf("format must be one of: hocr, pagexml, alto, txt")
	}
	if len(content) > maxExportedPageBytes {
		return "", "", "", fmt.Errorf("%w: page maximum is %d bytes", errItemExportOutputLimit, maxExportedPageBytes)
	}
	return content, mediaType, extension, nil
}

// handlePreparedItemExport dereferences a short-lived immutable export plan
// created by ItemService.PrepareItemExport. It never chooses revisions or
// accepts a mutable item/format query from the browser.
func (h *Handler) handlePreparedItemExport(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.currentWorkspaceID(r.Context())
	token, err := h.itemExportTokens.decode(strings.TrimSpace(r.PathValue("token")), workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "item export not found")
		return
	}
	ctx, finish := exportRequestContext(w, r)
	defer finish()
	release, allowed := h.exportLimiter.TryAcquire(fmt.Sprintf("workspace:%d", workspaceID))
	if !allowed {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "export concurrency limit exceeded")
		return
	}
	defer release()
	plan, err := h.loadCanonicalItemExportSnapshot(ctx, token.ItemID, token.Format, nil)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "item export generation timed out")
		case errors.Is(err, errItemExportRevisionConflict):
			writeError(w, http.StatusConflict, "canonical annotations changed; prepare the export again")
		case errors.Is(err, errItemExportSourceLimit):
			writeError(w, http.StatusRequestEntityTooLarge, "item export exceeds the source-byte limit")
		case errors.Is(err, errItemExportInvalid), errors.Is(err, store.ErrAnnotationPageNotFound):
			writeError(w, http.StatusNotFound, "item export not found")
		default:
			slog.Error("load prepared item export failed", "item_id", token.ItemID, "format", token.Format, "error_type", fmt.Sprintf("%T", err))
			writeError(w, http.StatusInternalServerError, "item export could not be loaded")
		}
		return
	}
	if plan.Digest != token.Digest {
		writeError(w, http.StatusConflict, "canonical annotations changed; prepare the export again")
		return
	}
	plan, err = h.loadCanonicalItemExportPages(ctx, plan)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "item export generation timed out")
		case errors.Is(err, errItemExportRevisionConflict):
			writeError(w, http.StatusConflict, "canonical annotations changed; prepare the export again")
		default:
			slog.Error("load prepared item export pages failed", "item_id", token.ItemID, "format", token.Format, "error_type", fmt.Sprintf("%T", err))
			writeError(w, http.StatusInternalServerError, "item export could not be loaded")
		}
		return
	}
	staged, cleanup, err := stageCanonicalItemExport(ctx, plan)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "item export generation timed out")
		case errors.Is(err, errItemExportOutputLimit):
			writeError(w, http.StatusRequestEntityTooLarge, "item export exceeds the output-byte limit")
		case errors.Is(err, errItemExportInvalid):
			writeError(w, http.StatusUnprocessableEntity, "item export could not be generated")
		case errors.Is(err, errItemExportStaging):
			slog.Error("stage prepared item export failed", "item_id", plan.Item.ID, "format", plan.Format, "error_type", fmt.Sprintf("%T", err))
			writeError(w, http.StatusInsufficientStorage, "item export staging is unavailable")
		default:
			slog.Error("prepare item export failed", "item_id", plan.Item.ID, "format", plan.Format, "error_type", fmt.Sprintf("%T", err))
			writeError(w, http.StatusInternalServerError, "item export could not be generated")
		}
		return
	}
	defer cleanup()
	w.Header().Set("Content-Type", plan.MediaType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", plan.Filename))
	w.Header().Set("Content-Length", strconv.FormatInt(staged.Size, 10))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	written, err := copyExportContent(ctx, w, staged.File)
	if err != nil {
		slog.Warn("stream prepared item export failed",
			"item_id", plan.Item.ID,
			"format", plan.Format,
			"bytes_written", written,
			"error_type", fmt.Sprintf("%T", err),
		)
	}
}
