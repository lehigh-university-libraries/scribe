package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	htrmetrics "github.com/lehigh-university-libraries/htr/pkg/metrics"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const (
	tripletMaxResourceBytes        = 8 << 20
	tripletRequestTimeout          = 15 * time.Second
	tripletPreconditionAttempts    = 4
	tripletChildWriteConcurrency   = 8
	annotationMirrorLeaseDuration  = 90 * time.Second
	annotationMirrorOperationLimit = 75 * time.Second
)

var errInvalidAnnotationPage = errors.New("invalid annotation page")

func (h *Handler) tripletPresentationInternalBase() string {
	return strings.TrimRight(strings.TrimSpace(config.Get().Config.Annotation.TripletPresentationInternalBase), "/")
}

func (h *Handler) publicAnnotationBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(config.Get().Config.Annotation.TripletPresentationBase), "/")
}

func (h *Handler) annotationPageIDForItemImage(itemImageID uint64) (string, error) {
	return iiif.CanonicalPageID(h.publicAnnotationBaseURL(), itemImageID)
}

func (h *Handler) tripletHTTPClient() *http.Client {
	return &http.Client{
		Timeout: tripletRequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func sameHTTPOrigin(left, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

// tripletURLForResourceID maps a public Presentation identifier to the exact
// in-cluster Triplet route. Only the configured public origin and path prefix
// are accepted; arbitrary resource IDs can never turn the mirror into an SSRF
// client.
func (h *Handler) tripletURLForResourceID(resourceID string) (string, bool, error) {
	internalRaw := h.tripletPresentationInternalBase()
	if internalRaw == "" {
		return "", false, nil
	}
	public, err := parseTripletBaseURL(h.publicAnnotationBaseURL())
	if err != nil {
		return "", false, fmt.Errorf("invalid Triplet public base")
	}
	internal, err := parseTripletBaseURL(internalRaw)
	if err != nil {
		return "", false, fmt.Errorf("invalid Triplet internal base")
	}
	resource, err := url.Parse(strings.TrimSpace(resourceID))
	if err != nil || resource.User != nil || resource.RawQuery != "" || resource.Fragment != "" || !sameHTTPOrigin(public, resource) {
		return "", false, fmt.Errorf("presentation resource is outside the Triplet namespace")
	}
	publicPath := strings.TrimRight(public.EscapedPath(), "/")
	resourcePath := resource.EscapedPath()
	if !strings.HasPrefix(resourcePath, publicPath+"/") {
		return "", false, fmt.Errorf("presentation resource is outside the Triplet namespace")
	}
	suffix := strings.TrimPrefix(resourcePath, publicPath)
	targetPath := strings.TrimRight(internal.EscapedPath(), "/") + suffix
	return internal.Scheme + "://" + internal.Host + targetPath, true, nil
}

func parseTripletBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("absolute HTTP(S) base URL is required")
	}
	return parsed, nil
}

type tripletRepresentation struct {
	exists  bool
	etag    string
	payload []byte
}

func (h *Handler) readTripletResource(ctx context.Context, resourceID string, includeBody bool) (tripletRepresentation, error) {
	reqURL, configured, err := h.tripletURLForResourceID(resourceID)
	if err != nil || !configured {
		return tripletRepresentation{}, err
	}
	method := http.MethodHead
	if includeBody {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return tripletRepresentation{}, fmt.Errorf("create Triplet presentation request")
	}
	resp, err := h.tripletHTTPClient().Do(req)
	if err != nil {
		return tripletRepresentation{}, fmt.Errorf("triplet presentation request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, tripletMaxResourceBytes+1))
		return tripletRepresentation{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, tripletMaxResourceBytes+1))
		return tripletRepresentation{}, fmt.Errorf("triplet presentation %s status %d", method, resp.StatusCode)
	}
	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	if !isStrongEntityTag(etag) {
		return tripletRepresentation{}, fmt.Errorf("triplet presentation response has no strong ETag")
	}
	representation := tripletRepresentation{exists: true, etag: etag}
	if !includeBody {
		return representation, nil
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, tripletMaxResourceBytes+1))
	if err != nil {
		return tripletRepresentation{}, fmt.Errorf("read Triplet presentation response")
	}
	if len(payload) > tripletMaxResourceBytes {
		return tripletRepresentation{}, fmt.Errorf("triplet presentation response exceeds limit")
	}
	representation.payload = payload
	return representation, nil
}

func isStrongEntityTag(value string) bool {
	return len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' && !strings.HasPrefix(value, "W/") && !strings.ContainsAny(value[1:len(value)-1], "\"\r\n")
}

func (h *Handler) putTripletResource(ctx context.Context, resource tripletPresentationResource) error {
	if len(resource.Payload) == 0 || len(resource.Payload) > tripletMaxResourceBytes {
		return fmt.Errorf("triplet presentation request exceeds limit")
	}
	reqURL, configured, err := h.tripletURLForResourceID(resource.ID)
	if err != nil || !configured {
		return err
	}
	for range tripletPreconditionAttempts {
		current, err := h.readTripletResource(ctx, resource.ID, true)
		if err != nil {
			return err
		}
		if current.exists && bytes.Equal(current.payload, resource.Payload) {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(resource.Payload))
		if err != nil {
			return fmt.Errorf("create Triplet presentation request")
		}
		req.Header.Set("Content-Type", "application/ld+json")
		if current.exists {
			req.Header.Set("If-Match", current.etag)
		} else {
			req.Header.Set("If-None-Match", "*")
		}
		if token := strings.TrimSpace(config.Get().Config.Annotation.TripletPresentationWriteToken); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := h.tripletHTTPClient().Do(req)
		if err != nil {
			return fmt.Errorf("triplet presentation request failed")
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, tripletMaxResourceBytes+1))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusNoContent {
			return nil
		}
		if resp.StatusCode != http.StatusPreconditionFailed {
			return fmt.Errorf("triplet presentation PUT status %d", resp.StatusCode)
		}
	}
	return fmt.Errorf("triplet presentation PUT remained contended")
}

func (h *Handler) deleteTripletResource(ctx context.Context, resourceID string) error {
	reqURL, configured, err := h.tripletURLForResourceID(resourceID)
	if err != nil || !configured {
		return err
	}
	for range tripletPreconditionAttempts {
		current, err := h.readTripletResource(ctx, resourceID, false)
		if err != nil {
			return err
		}
		if !current.exists {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
		if err != nil {
			return fmt.Errorf("create Triplet presentation request")
		}
		req.Header.Set("If-Match", current.etag)
		if token := strings.TrimSpace(config.Get().Config.Annotation.TripletPresentationWriteToken); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := h.tripletHTTPClient().Do(req)
		if err != nil {
			return fmt.Errorf("triplet presentation request failed")
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, tripletMaxResourceBytes+1))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			return nil
		}
		if resp.StatusCode != http.StatusPreconditionFailed {
			return fmt.Errorf("triplet presentation DELETE status %d", resp.StatusCode)
		}
	}
	return fmt.Errorf("triplet presentation DELETE remained contended")
}

func (h *Handler) deleteTripletImageGraph(ctx context.Context, itemImageID uint64) error {
	pageID, err := h.annotationPageIDForItemImage(itemImageID)
	if err != nil {
		return err
	}
	page, err := h.readTripletResource(ctx, pageID, true)
	if err != nil {
		return err
	}
	childIDs := make([]string, 0)
	if page.exists {
		children, err := iiif.AnnotationsFromPage(page.payload)
		if err != nil {
			return fmt.Errorf("decode Triplet AnnotationPage graph")
		}
		childIDs = make([]string, 0, len(children))
		for _, child := range children {
			childIDs = append(childIDs, child.ID)
		}
	}
	singleManifestID, err := iiif.ItemImageManifestID(h.publicAnnotationBaseURL(), itemImageID)
	if err != nil {
		return err
	}
	canvasID, err := iiif.ItemImageCanvasID(h.publicAnnotationBaseURL(), itemImageID)
	if err != nil {
		return err
	}
	// Remove public entry points first, but keep the page until every dynamic
	// child is gone so a retry can rediscover any unfinished child deletions.
	for _, resourceID := range []string{singleManifestID, canvasID} {
		if err := h.deleteTripletResource(ctx, resourceID); err != nil {
			return err
		}
	}
	if err := h.deleteTripletResourcesParallel(ctx, childIDs); err != nil {
		return err
	}
	paintingPageID, err := iiif.PaintingPageID(h.publicAnnotationBaseURL(), itemImageID)
	if err != nil {
		return err
	}
	paintingAnnotationID, err := iiif.PaintingAnnotationID(h.publicAnnotationBaseURL(), itemImageID)
	if err != nil {
		return err
	}
	for _, resourceID := range []string{pageID, paintingPageID, paintingAnnotationID} {
		if err := h.deleteTripletResource(ctx, resourceID); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) deleteTripletResourcesParallel(ctx context.Context, resourceIDs []string) error {
	if len(resourceIDs) == 0 {
		return nil
	}
	jobs := make(chan string)
	errs := make(chan error, len(resourceIDs))
	var workers sync.WaitGroup
	workerCount := min(tripletChildWriteConcurrency, len(resourceIDs))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for resourceID := range jobs {
				if err := h.deleteTripletResource(ctx, resourceID); err != nil {
					errs <- err
				}
			}
		}()
	}
	for _, resourceID := range resourceIDs {
		select {
		case jobs <- resourceID:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			close(errs)
			return fmt.Errorf("triplet presentation deletion deadline exceeded")
		}
	}
	close(jobs)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) reconcileTripletItemGraph(ctx context.Context, itemID string) error {
	manifestID, err := iiif.ItemManifestID(h.publicAnnotationBaseURL(), itemID)
	if err != nil {
		return err
	}
	resource, err := h.buildPublishedItemManifest(ctx, itemID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return h.deleteTripletResource(ctx, manifestID)
		}
		return err
	}
	if resource == nil {
		return h.deleteTripletResource(ctx, manifestID)
	}
	return h.putTripletResource(ctx, *resource)
}

func (h *Handler) publishTripletResources(ctx context.Context, resources []tripletPresentationResource) error {
	if len(resources) == 0 {
		return fmt.Errorf("triplet publication graph is empty")
	}
	parallel := 0
	for parallel < len(resources) && resources[parallel].Parallel {
		parallel++
	}
	if parallel > 0 {
		jobs := make(chan tripletPresentationResource)
		errs := make(chan error, parallel)
		var workers sync.WaitGroup
		workerCount := min(tripletChildWriteConcurrency, parallel)
		for range workerCount {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for resource := range jobs {
					if err := h.putTripletResource(ctx, resource); err != nil {
						errs <- err
					}
				}
			}()
		}
		for _, resource := range resources[:parallel] {
			jobs <- resource
		}
		close(jobs)
		workers.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
	}
	for _, resource := range resources[parallel:] {
		if err := h.putTripletResource(ctx, resource); err != nil {
			return err
		}
	}
	return nil
}

// deliverAnnotationMirror materializes one image-scoped graph, then drains the
// durable standalone-Annotation tombstones that are not children of that exact
// desired page. The parent becomes current before removed children disappear;
// a crash or failed DELETE leaves the tombstone row for an idempotent retry.
func (h *Handler) deliverAnnotationMirror(ctx context.Context, delivery store.AnnotationMirrorDelivery) error {
	resources, err := h.buildPublishedPresentationResources(ctx, delivery.ItemImageID)
	if err != nil {
		return err
	}
	tombstones, err := h.annotations.LoadAnnotationMirrorTombstones(ctx, delivery.ItemImageID)
	if err != nil {
		return err
	}
	desiredChildren := make(map[string]struct{})
	for _, resource := range resources {
		if resource.Parallel {
			desiredChildren[resource.ID] = struct{}{}
		}
	}
	removedChildren := make([]string, 0, len(tombstones.AnnotationIDs))
	for _, annotationID := range tombstones.AnnotationIDs {
		if _, desired := desiredChildren[annotationID]; !desired {
			removedChildren = append(removedChildren, annotationID)
		}
	}
	if err := h.publishTripletResources(ctx, resources); err != nil {
		return err
	}
	if err := h.deleteTripletResourcesParallel(ctx, removedChildren); err != nil {
		return err
	}
	if err := h.annotations.AcknowledgeAnnotationMirrorTombstones(ctx, delivery.ItemImageID, removedChildren); err != nil {
		return err
	}
	return nil
}

// StartAnnotationMirrorDispatcher delivers the coalescing mirror outbox. The
// publication transaction owns enqueueing, so a process crash can delay a
// Triplet update but cannot lose the intent. Revision/lease fencing prevents an
// older worker from deleting a newer pending payload.
func (h *Handler) StartAnnotationMirrorDispatcher(ctx context.Context) {
	if h.annotations == nil || h.tripletPresentationInternalBase() == "" {
		return
	}
	h.startBackgroundWorker(func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			h.dispatchAnnotationMirrors(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (h *Handler) dispatchAnnotationMirrors(ctx context.Context) {
	for range 10 {
		delivery, err := h.annotations.ClaimAnnotationMirror(ctx, annotationMirrorLeaseDuration)
		if err != nil {
			slog.Warn("failed to claim annotation mirror", "error_type", safeLogErrorType(err))
			return
		}
		if delivery == nil {
			return
		}
		operationCtx, cancel := context.WithTimeout(ctx, annotationMirrorOperationLimit)
		buildErr := h.deliverAnnotationMirror(operationCtx, *delivery)
		cancel()
		if buildErr != nil {
			delay := annotationMirrorRetryDelay(delivery.Attempt)
			retryErr := h.annotations.RetryAnnotationMirror(ctx, *delivery, buildErr, time.Now().UTC().Add(delay))
			if retryErr != nil && !errors.Is(retryErr, store.ErrAnnotationMirrorLease) {
				slog.Warn("failed to defer annotation mirror", "item_image_id", delivery.ItemImageID, "revision", delivery.Revision, "error_type", safeLogErrorType(retryErr))
			}
			slog.Warn("annotation mirror delivery failed", "item_image_id", delivery.ItemImageID, "revision", delivery.Revision, "attempt", delivery.Attempt, "max_attempts", delivery.MaxAttempts, "retry_in", delay, "failure", store.SafeAnnotationMirrorFailureMessage(buildErr))
			continue
		}
		if err := h.annotations.CompleteAnnotationMirror(ctx, *delivery); err != nil && !errors.Is(err, store.ErrAnnotationMirrorLease) {
			slog.Warn("failed to complete annotation mirror", "item_image_id", delivery.ItemImageID, "revision", delivery.Revision, "error_type", safeLogErrorType(err))
		}
	}
}

func annotationMirrorRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	delay := time.Second * time.Duration(1<<(attempt-1))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func (h *Handler) currentAnnotationPage(ctx context.Context, itemImageID uint64) (store.AnnotationPage, error) {
	if _, err := h.itemImageForRequest(ctx, itemImageID); err != nil {
		if isNotFoundError(err) {
			return store.AnnotationPage{}, store.ErrAnnotationPageNotFound
		}
		return store.AnnotationPage{}, fmt.Errorf("load annotation page item image: %w", err)
	}
	return h.annotations.LoadPage(ctx, h.currentWorkspaceID(ctx), itemImageID)
}

func (h *Handler) saveCanonicalAnnotationPage(ctx context.Context, itemImageID uint64, raw string, expectedRevision uint64) (store.AnnotationPage, error) {
	image, err := h.itemImageForRequest(ctx, itemImageID)
	if err != nil {
		return store.AnnotationPage{}, store.ErrAnnotationPageResource
	}
	canvasURI := strings.TrimSpace(image.CanvasURI)
	if canvasURI == "" {
		return store.AnnotationPage{}, store.ErrAnnotationPageResource
	}
	identity := iiif.PageIdentity{
		PublicBaseURL: h.publicAnnotationBaseURL(),
		ItemImageID:   itemImageID,
		CanvasURI:     canvasURI,
	}
	normalized, err := iiif.NormalizeAnnotationPage([]byte(raw), identity)
	if err != nil {
		return store.AnnotationPage{}, fmt.Errorf("%w: %v", errInvalidAnnotationPage, err)
	}
	// A line and its word annotations are two views of one correction. Resolve
	// whichever side the editor changed before persistence so reload, public
	// dereference, search, and every export observe the same text.
	if expectedRevision > 0 {
		current, loadErr := h.annotations.LoadPage(ctx, h.currentWorkspaceID(ctx), itemImageID)
		if loadErr != nil {
			return store.AnnotationPage{}, loadErr
		}
		var currentDocument, proposedDocument map[string]any
		if err := iiif.DecodeJSON([]byte(current.Payload), &currentDocument); err != nil {
			return store.AnnotationPage{}, fmt.Errorf("decode current canonical page: %w", err)
		}
		if err := iiif.DecodeJSON(normalized, &proposedDocument); err != nil {
			return store.AnnotationPage{}, fmt.Errorf("decode proposed canonical page: %w", err)
		}
		currentItems, currentOK := currentDocument["items"].([]any)
		proposedItems, proposedOK := proposedDocument["items"].([]any)
		if !currentOK || !proposedOK {
			return store.AnnotationPage{}, fmt.Errorf("%w: canonical page items must be an array", errInvalidAnnotationPage)
		}
		reconciledItems, reconcileErr := reconcileEditedLineWords(currentItems, proposedItems, identity, expectedRevision)
		if reconcileErr != nil {
			return store.AnnotationPage{}, fmt.Errorf("%w: reconcile line and word annotations: %v", errInvalidAnnotationPage, reconcileErr)
		}
		proposedDocument["items"] = reconciledItems
		reconciled, marshalErr := json.Marshal(proposedDocument)
		if marshalErr != nil {
			return store.AnnotationPage{}, fmt.Errorf("encode reconciled canonical page: %w", marshalErr)
		}
		normalized, err = iiif.NormalizeAnnotationPage(reconciled, identity)
		if err != nil {
			return store.AnnotationPage{}, fmt.Errorf("%w: %v", errInvalidAnnotationPage, err)
		}
	}
	if err := iiif.ValidateAnnotationPageGeometry(normalized, image.Width, image.Height); err != nil {
		return store.AnnotationPage{}, fmt.Errorf("%w: %v", errInvalidAnnotationPage, err)
	}
	pageID, err := h.annotationPageIDForItemImage(itemImageID)
	if err != nil {
		return store.AnnotationPage{}, err
	}
	userID := h.currentUserID(ctx)
	page := store.AnnotationPage{
		WorkspaceID:     h.currentWorkspaceID(ctx),
		ItemImageID:     itemImageID,
		PageID:          pageID,
		CanvasURI:       canvasURI,
		Payload:         string(normalized),
		UpdatedByUserID: &userID,
	}
	var correctionMetric *store.AnnotationCorrectionMetric
	if expectedRevision > 0 && h.ocrRuns != nil {
		run, runErr := h.ocrRuns.GetByItemImageID(ctx, itemImageID)
		switch {
		case runErr == nil:
			lines, _, _, conversionErr := annotationPageToHOCRLines(string(normalized))
			if conversionErr != nil {
				return store.AnnotationPage{}, fmt.Errorf("%w: cannot derive correction text: %v", errInvalidAnnotationPage, conversionErr)
			}
			correctionMetric = &store.AnnotationCorrectionMetric{
				LevenshteinDistance: htrmetrics.LevenshteinDistance(
					normalizeCorrectionMetricText(run.OriginalText),
					normalizeCorrectionMetricText(linesToPlainText(lines)),
				),
			}
		case errors.Is(runErr, sql.ErrNoRows):
			// Pages imported without an OCR run remain valid canonical resources;
			// there is simply no model baseline against which to score them.
		default:
			return store.AnnotationPage{}, fmt.Errorf("load OCR baseline: %w", runErr)
		}
	}
	saved, err := h.annotations.SavePageWithCorrectionMetric(ctx, page, expectedRevision, correctionMetric)
	if err != nil {
		return store.AnnotationPage{}, err
	}
	return saved, nil
}

func normalizeCorrectionMetricText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func annotationItemsFromPage(raw string) ([]any, error) {
	var page struct {
		Items []any `json:"items"`
	}
	if err := iiif.DecodeJSON([]byte(raw), &page); err != nil {
		return nil, fmt.Errorf("decode annotation page: %w", err)
	}
	return page.Items, nil
}
