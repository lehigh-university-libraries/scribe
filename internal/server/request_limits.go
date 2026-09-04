package server

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/worklimit"
)

const (
	requestRatePerSecond = 4.0
	requestBurst         = 80.0
	maxRateLimitKeys     = 10_000
	maxSSEConnections    = 200
	maxSSEPerWorkspace   = 10
	maxDefaultBodyBytes  = 4 << 20
	maxAnnotationBytes   = 32 << 20
	maxImageRequestBytes = 140 << 20

	maxConcurrentLargeBodies          = 4
	maxConcurrentLargeBodiesPerClient = 2
	maxConcurrentExports              = 4
	maxConcurrentExportsPerWorkspace  = 2
	maxConcurrentCanonicalReads       = 4
	maxConcurrentCanonicalReadsPerKey = 2
	edgeRequestRatePerSecond          = 2.0
	// One first-party editor session performs a bounded RPC burst while the
	// shell, transcription catch-up, Mirador adapter, and status UI converge.
	// Match the authenticated per-user burst so the pre-authentication boundary
	// does not reject traffic the inner boundary admits. The sustained edge rate
	// and independent aggregate-IP admission boundary remain unchanged.
	edgeRequestBurst           = requestBurst
	edgeAggregateRatePerSecond = 12.0
	edgeAggregateBurst         = 120.0
)

func newProcessingLimiter(cfg config.ProcessingConfig) *worklimit.HierarchicalLimiter {
	if cfg.GlobalConcurrency == 0 {
		cfg.GlobalConcurrency = 4
	}
	if cfg.PerWorkspaceConcurrency == 0 {
		cfg.PerWorkspaceConcurrency = 2
	}
	if cfg.PerProviderConcurrency == 0 {
		cfg.PerProviderConcurrency = 2
	}
	limiter, err := worklimit.NewHierarchical(
		cfg.GlobalConcurrency,
		cfg.PerWorkspaceConcurrency,
		cfg.PerProviderConcurrency,
	)
	if err == nil {
		return limiter
	}
	slog.Warn("invalid processing concurrency; using safe defaults", "error_type", safeLogErrorType(err))
	limiter, _ = worklimit.NewHierarchical(4, 2, 2)
	return limiter
}

type requestBucket struct {
	tokens   float64
	lastSeen time.Time
}

type requestLimiter struct {
	mu      sync.Mutex
	buckets map[string]requestBucket
	now     func() time.Time
	rate    float64
	burst   float64
}

func newRequestLimiter() *requestLimiter {
	return newRequestLimiterWithPolicy(requestRatePerSecond, requestBurst)
}

func newEdgeRequestLimiter() *requestLimiter {
	return newRequestLimiterWithPolicy(edgeRequestRatePerSecond, edgeRequestBurst)
}

func newEdgeAggregateLimiter() *requestLimiter {
	return newRequestLimiterWithPolicy(edgeAggregateRatePerSecond, edgeAggregateBurst)
}

func newRequestLimiterWithPolicy(rate, burst float64) *requestLimiter {
	return &requestLimiter{buckets: make(map[string]requestBucket), now: time.Now, rate: rate, burst: burst}
}

func (l *requestLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	bucket, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxRateLimitKeys {
			cutoff := now.Add(-10 * time.Minute)
			for candidate, stale := range l.buckets {
				if stale.lastSeen.Before(cutoff) {
					delete(l.buckets, candidate)
				}
			}
			if len(l.buckets) >= maxRateLimitKeys {
				return false
			}
		}
		bucket = requestBucket{tokens: l.burst, lastSeen: now}
	}
	elapsed := now.Sub(bucket.lastSeen).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	bucket.tokens += elapsed * l.rate
	if bucket.tokens > l.burst {
		bucket.tokens = l.burst
	}
	bucket.lastSeen = now
	allowed := bucket.tokens >= 1
	if allowed {
		bucket.tokens--
	}
	l.buckets[key] = bucket
	return allowed
}

func (h *Handler) requestLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/livez":
			next.ServeHTTP(w, r)
			return
		}
		key := requestLimitKey(r)
		if h.requestLimiter != nil && !h.requestLimiter.Allow(key) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "request rate limit exceeded")
			return
		}
		if isHeavyCanonicalRead(r) && h.canonicalReadLimiter != nil {
			release, allowed := h.canonicalReadLimiter.TryAcquire(canonicalReadAdmissionKey(r))
			if !allowed {
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusTooManyRequests, "canonical page read concurrency limit exceeded")
				return
			}
			defer release()
		}
		next.ServeHTTP(w, r)
	})
}

func isHeavyCanonicalRead(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch r.URL.Path {
	case "/scribe.v1.AnnotationService/GetAnnotationPage",
		"/scribe.v1.AnnotationService/GetAnnotation",
		"/scribe.v1.AnnotationService/SearchAnnotations":
		return true
	default:
		return false
	}
}

func canonicalReadAdmissionKey(r *http.Request) string {
	if principal, ok := auth.PrincipalFromContext(r.Context()); ok && !principal.Anonymous() {
		return fmt.Sprintf("workspace:%d", principal.WorkspaceID)
	}
	return "client:" + edgeClientIP(r)
}

// requestAdmissionMiddleware is the unauthenticated edge admission boundary.
// It must wrap credential verification because API-key and external-JWT
// authentication can require database or network work.
func (h *Handler) requestAdmissionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/livez":
			next.ServeHTTP(w, r)
			return
		}
		// Preserve an aggregate ceiling for one public address, but do not make
		// every authenticated user behind an institutional NAT consume the same
		// tiny pre-authentication bucket. A bounded hash of the credential/session
		// separates normal users without retaining or logging secret material.
		// Rotating bogus credentials cannot bypass the aggregate IP ceiling.
		if h.edgeAggregateLimiter != nil && !h.edgeAggregateLimiter.Allow(edgeAggregateLimitKey(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "request rate limit exceeded")
			return
		}
		if h.edgeRequestLimiter != nil && !h.edgeRequestLimiter.Allow(edgeRequestLimitKey(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "request rate limit exceeded")
			return
		}
		if hasCompressedRPCRequestBody(r) {
			writeError(w, http.StatusUnsupportedMediaType, "compressed RPC request bodies are not supported")
			return
		}
		var releaseReadiness func()
		if (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") && h.readinessLimiter != nil {
			var allowed bool
			releaseReadiness, allowed = h.readinessLimiter.TryAcquire(auth.ClientIP(r))
			if !allowed {
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusTooManyRequests, "readiness concurrency limit exceeded")
				return
			}
			defer releaseReadiness()
		}
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			limit := requestBodyLimit(r.URL.Path)
			if r.ContentLength > limit {
				writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		var release func()
		// Every request class above the small default can retain multiple copies
		// while Connect decodes and the IIIF boundary validates it. Share one
		// bounded admission pool so annotation requests cannot bypass the upload
		// memory guard merely because their per-request cap is smaller.
		if requestBodyLimit(r.URL.Path) > maxDefaultBodyBytes && h.largeBodyLimiter != nil {
			var allowed bool
			release, allowed = h.largeBodyLimiter.TryAcquire(auth.ClientIP(r))
			if !allowed {
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusTooManyRequests, "large request concurrency limit exceeded")
				return
			}
			defer release()
		}
		next.ServeHTTP(w, r)
	})
}

func edgeRequestLimitKey(r *http.Request) string {
	class := requestLimitClass(r.URL.Path)
	if fingerprint, ok := edgeCredentialFingerprint(r); ok {
		return "edge:credential:" + fingerprint + ":class:" + class
	}
	return "edge:anonymous:" + edgeClientIP(r) + ":class:" + class
}

func edgeAggregateLimitKey(r *http.Request) string {
	return "edge:aggregate:" + edgeClientIP(r)
}

func edgeClientIP(r *http.Request) string {
	host := auth.ClientIP(r)
	if host == "" {
		host = "unknown"
	}
	return host
}

func edgeCredentialFingerprint(r *http.Request) (string, bool) {
	var kind, credential string
	if raw := strings.TrimSpace(r.Header.Get("X-Scribe-API-Key")); raw != "" {
		kind, credential = "api-key", raw
	} else if raw := strings.TrimSpace(r.Header.Get("Authorization")); len(raw) > 7 && strings.EqualFold(raw[:7], "bearer ") && strings.TrimSpace(raw[7:]) != "" {
		kind, credential = "bearer", strings.TrimSpace(raw[7:])
	} else {
		cookieName := strings.TrimSpace(config.Get().Config.Auth.CookieName)
		if cookieName == "" {
			cookieName = "scribe_session"
		}
		if cookie, err := r.Cookie(cookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
			kind, credential = "session", strings.TrimSpace(cookie.Value)
		}
	}
	if credential == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + credential))
	return fmt.Sprintf("%x", sum[:16]), true
}

// hasCompressedRPCRequestBody rejects request compression before Connect can
// inflate a message. The HTTP body limits can therefore enforce the actual
// protobuf/JSON bytes accepted by each procedure instead of only the smaller
// compressed representation. Response compression remains supported.
func hasCompressedRPCRequestBody(r *http.Request) bool {
	if r == nil || !strings.HasPrefix(r.URL.Path, "/scribe.v1.") {
		return false
	}
	for _, name := range []string{"Content-Encoding", "Connect-Content-Encoding", "Grpc-Encoding"} {
		for _, value := range r.Header.Values(name) {
			for _, encoding := range strings.Split(value, ",") {
				normalized := strings.ToLower(strings.TrimSpace(encoding))
				if normalized != "" && normalized != "identity" {
					return true
				}
			}
		}
	}
	return false
}

func requestBodyLimit(path string) int64 {
	switch path {
	case "/scribe.v1.ImageProcessingService/ProcessHOCR", "/scribe.v1.ItemService/UploadItemImage":
		return maxImageRequestBytes
	case "/scribe.v1.AnnotationService/SaveAnnotationPage",
		"/scribe.v1.AnnotationService/EnrichAnnotation",
		"/scribe.v1.AnnotationService/SplitLineIntoWords",
		"/scribe.v1.AnnotationService/SplitPageIntoWords",
		"/scribe.v1.AnnotationService/SplitLineIntoTwoLines",
		"/scribe.v1.AnnotationService/JoinLines",
		"/scribe.v1.AnnotationService/JoinWordsIntoLine":
		return maxAnnotationBytes
	default:
		return maxDefaultBodyBytes
	}
}

func requestLimitKey(r *http.Request) string {
	class := requestLimitClass(r.URL.Path)
	if principal, ok := auth.PrincipalFromContext(r.Context()); ok && !principal.Anonymous() {
		return fmt.Sprintf("workspace:%d:user:%d:class:%s", principal.WorkspaceID, principal.UserID, class)
	}
	host := auth.ClientIP(r)
	if host == "" {
		host = "unknown"
	}
	return "remote:" + host + ":class:" + class
}

func requestLimitClass(path string) string {
	switch {
	case strings.HasPrefix(path, "/auth/"), strings.HasPrefix(path, "/scribe.v1.AuthService/"):
		return "auth"
	case path == "/scribe.v1.ImageProcessingService/ProcessHOCR", path == "/scribe.v1.ItemService/UploadItemImage":
		return "upload"
	case path == "/scribe.v1.AnnotationService/ExportAnnotationPage", path == "/scribe.v1.ItemService/PrepareItemExport", strings.HasPrefix(path, "/v1/item-exports/"), strings.HasSuffix(path, "/hocr"):
		return "export"
	case strings.HasPrefix(path, "/scribe.v1."):
		return "rpc"
	case path == "/v1/events":
		return "events"
	default:
		return "web"
	}
}

// bodyConcurrencyLimiter bounds requests that can retain a complete large
// protobuf byte field in memory. It is deliberately non-blocking: queued HTTP
// bodies would still consume sockets and proxy buffers without making useful
// progress.
type bodyConcurrencyLimiter struct {
	mu             sync.Mutex
	globalLimit    int
	perClientLimit int
	active         int
	byClient       map[string]int
}

func newBodyConcurrencyLimiter(globalLimit, perClientLimit int) *bodyConcurrencyLimiter {
	return &bodyConcurrencyLimiter{
		globalLimit: globalLimit, perClientLimit: perClientLimit, byClient: make(map[string]int),
	}
}

func (l *bodyConcurrencyLimiter) TryAcquire(client string) (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	client = strings.TrimSpace(client)
	if client == "" {
		client = "unknown"
	}
	l.mu.Lock()
	if l.globalLimit < 1 || l.perClientLimit < 1 || l.active >= l.globalLimit || l.byClient[client] >= l.perClientLimit {
		l.mu.Unlock()
		return nil, false
	}
	l.active++
	l.byClient[client]++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.active--
			l.byClient[client]--
			if l.byClient[client] == 0 {
				delete(l.byClient, client)
			}
		})
	}, true
}

type connectionLimiter struct {
	mu          sync.Mutex
	total       int
	byWorkspace map[uint64]int
}

func newConnectionLimiter() *connectionLimiter {
	return &connectionLimiter{byWorkspace: make(map[uint64]int)}
}

func (l *connectionLimiter) Acquire(workspaceID uint64) (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= maxSSEConnections || l.byWorkspace[workspaceID] >= maxSSEPerWorkspace {
		return nil, false
	}
	l.total++
	l.byWorkspace[workspaceID]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.total--
		l.byWorkspace[workspaceID]--
		if l.byWorkspace[workspaceID] == 0 {
			delete(l.byWorkspace, workspaceID)
		}
	}, true
}
