package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

var (
	// ErrAnnotationPageNotFound means the tenant-scoped canonical page does not exist.
	ErrAnnotationPageNotFound = errors.New("annotation page not found")
	// ErrAnnotationRevisionConflict means a create or update used a stale revision.
	ErrAnnotationRevisionConflict = errors.New("annotation page revision conflict")
	// ErrAnnotationPageResource means the image does not belong to the workspace.
	ErrAnnotationPageResource = errors.New("annotation page resource does not belong to workspace")
	// ErrAnnotationJobFence means a worker no longer owns the active job lease.
	ErrAnnotationJobFence = ErrTranscriptionJobFence
)

// AnnotationPage is the canonical IIIF Presentation 3 correction resource.
type AnnotationPage struct {
	WorkspaceID     uint64
	ItemImageID     uint64
	PageID          string
	CanvasURI       string
	Payload         string
	Revision        uint64
	UpdatedByUserID *uint64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// AnnotationPageRevision is the bounded canonical snapshot identity exposed
// with an item. Page payloads remain available only through annotation reads.
type AnnotationPageRevision struct {
	ItemImageID  uint64
	Revision     uint64
	PayloadBytes uint64
}

// AnnotationManifestReference is the bounded page identity needed to compose
// an aggregate manifest. It intentionally excludes the canonical payload.
type AnnotationManifestReference struct {
	WorkspaceID uint64
	ItemImageID uint64
	PageID      string
	CanvasURI   string
	Revision    uint64
	ModifiedAt  time.Time
	Published   bool
}

// PublishedAnnotationPage is the public snapshot of one canonical revision.
// It changes only through PublishPage; ordinary draft saves never affect it.
type PublishedAnnotationPage struct {
	WorkspaceID       uint64
	ItemImageID       uint64
	PageID            string
	CanvasURI         string
	Payload           string
	PublishedRevision uint64
	PublishedByUserID *uint64
	PublishedAt       time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	NewlyPublished    bool
}

// AnnotationPublicationOptions binds a revision-checked public snapshot to its
// durable CloudEvent and webhook deliveries in the same transaction.
type AnnotationPublicationOptions struct {
	ExpectedRevision  uint64
	PublishedByUserID *uint64
	EventID           string
	EventType         string
	Subject           string
}

// AnnotationIndexEntry is a non-canonical query projection of one annotation.
type AnnotationIndexEntry struct {
	WorkspaceID     uint64
	ItemImageID     uint64
	ID              string
	CanvasURI       string
	TextGranularity string
	Position        uint32
	Payload         string
}

// AnnotationCorrectionMetric is derived from a canonical page immediately
// before it is persisted. Keeping the metric update in the page transaction
// prevents a revision and its correction score from drifting apart.
type AnnotationCorrectionMetric struct {
	LevenshteinDistance int
}

// AnnotationJobCompletion is committed with the canonical page so a crash
// cannot leave a retryable job after its output page has already been saved.
type AnnotationJobCompletion struct {
	TranscriptionAttemptFence
	EventID   string
	EventType string
	Subject   string
	BodyJSON  string
	// OCRRun is the immutable baseline/provenance derived from this exact page
	// revision. When present it is committed and quota-accounted in the same
	// transaction as the page and terminal job state.
	OCRRun *OCRRun
}

// AnnotationReprocessCommit is the complete database-side result of one
// successful segmentation call. The provider call happens before this commit;
// the canonical CAS, immutable OCR baseline, replacement transcription job,
// and durable event either all commit or all roll back.
type AnnotationReprocessCommit struct {
	OCRRun          OCRRun
	Context         Context
	EventID         string
	EventType       string
	Subject         string
	BodyJSON        string
	ExternalRequest *AnnotationReprocessExternalRequest
}

// AnnotationReprocessExternalRequest fences the operation-level reservation
// acquired before segmentation. Completion is written in the same transaction
// as the canonical page, so a lost HTTP response can be replayed without a
// second provider call.
type AnnotationReprocessExternalRequest struct {
	Source         string
	IdempotencyKey string
	LeaseOwner     string
}

type AnnotationReprocessResult struct {
	Page               AnnotationPage
	TranscriptionJobID uint64
}

type annotationPageSaveOptions struct {
	metric     *AnnotationCorrectionMetric
	fence      *TranscriptionAttemptFence
	completion *AnnotationJobCompletion
	reprocess  *AnnotationReprocessCommit
}

type annotationPageSaveResult struct {
	page               AnnotationPage
	transcriptionJobID uint64
}

// AnnotationStore atomically persists canonical pages and their derived index.
type AnnotationStore struct {
	pool      *sql.DB
	q         *db.Queries
	admission TranscriptionJobAdmissionPolicy
	quota     StorageQuotaLimits
}

// NewAnnotationStore creates a canonical annotation repository.
func NewAnnotationStore(pool *sql.DB) *AnnotationStore {
	return NewAnnotationStoreWithTranscriptionAdmission(pool, defaultTranscriptionJobAdmissionPolicy())
}

// NewAnnotationStoreWithTranscriptionAdmission creates a canonical repository
// whose atomic reprocess path uses the supplied durable job admission policy.
func NewAnnotationStoreWithTranscriptionAdmission(pool *sql.DB, admission TranscriptionJobAdmissionPolicy) *AnnotationStore {
	return &AnnotationStore{pool: pool, q: db.New(pool), admission: admission, quota: unboundedStorageQuotaLimits()}
}

// SetStorageQuotaLimits installs the durable payload growth policy used by
// page edits, reprocessing, worker completion, and publication.
func (s *AnnotationStore) SetStorageQuotaLimits(limits StorageQuotaLimits) error {
	if s == nil {
		return fmt.Errorf("annotation store is not configured")
	}
	if err := validateStorageQuotaLimits(limits); err != nil {
		return err
	}
	s.quota = limits
	return nil
}

// LoadPage returns one page by its tenant-scoped image identity.
func (s *AnnotationStore) LoadPage(ctx context.Context, workspaceID, itemImageID uint64) (AnnotationPage, error) {
	if s == nil || s.q == nil {
		return AnnotationPage{}, ErrAnnotationPageNotFound
	}
	row, err := s.q.GetAnnotationPage(ctx, workspaceID, itemImageID)
	return annotationPageFromRow(row, err)
}

// ListItemRevisions returns all committed canonical page revisions for one
// workspace-scoped item in image order using a single bounded database query.
func (s *AnnotationStore) ListItemRevisions(ctx context.Context, workspaceID uint64, itemID string) ([]AnnotationPageRevision, error) {
	if s == nil || s.q == nil || workspaceID == 0 || strings.TrimSpace(itemID) == "" {
		return nil, fmt.Errorf("list item annotation revisions: store, workspace, and item are required")
	}
	rows, err := s.q.ListItemAnnotationRevisionsManual(ctx, db.ListItemAnnotationRevisionsManualParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
	})
	if err != nil {
		return nil, fmt.Errorf("list item annotation revisions: %w", err)
	}
	revisions := make([]AnnotationPageRevision, 0, len(rows))
	for _, row := range rows {
		if row.ItemImageID == 0 || row.Revision == 0 || row.PayloadBytes < 0 {
			return nil, fmt.Errorf("list item annotation revisions: invalid persisted revision identity")
		}
		revisions = append(revisions, AnnotationPageRevision{
			ItemImageID:  row.ItemImageID,
			Revision:     row.Revision,
			PayloadBytes: uint64(row.PayloadBytes),
		})
	}
	return revisions, nil
}

// ListItemManifestReferences returns every draft page identity for one item in
// a single query without materializing annotation payloads.
func (s *AnnotationStore) ListItemManifestReferences(ctx context.Context, workspaceID uint64, itemID string) ([]AnnotationManifestReference, error) {
	if s == nil || s.q == nil || workspaceID == 0 || strings.TrimSpace(itemID) == "" {
		return nil, fmt.Errorf("list item annotation manifest references: store, workspace, and item are required")
	}
	rows, err := s.q.ListItemAnnotationManifestReferencesManual(ctx, db.ListItemAnnotationManifestReferencesManualParams{
		WorkspaceID: workspaceID,
		ItemID:      strings.TrimSpace(itemID),
	})
	if err != nil {
		return nil, fmt.Errorf("list item annotation manifest references: %w", err)
	}
	references := make([]AnnotationManifestReference, 0, len(rows))
	for _, row := range rows {
		reference := AnnotationManifestReference{
			WorkspaceID: row.WorkspaceID,
			ItemImageID: row.ItemImageID,
			PageID:      row.PageID,
			CanvasURI:   row.CanvasUri,
			Revision:    row.Revision,
			ModifiedAt:  row.UpdatedAt,
		}
		if err := validateAnnotationManifestReference(reference); err != nil {
			return nil, fmt.Errorf("list item annotation manifest references: %w", err)
		}
		references = append(references, reference)
	}
	return references, nil
}

// ListPublishedItemManifestReferences returns the explicit public snapshots
// for one globally unique item ID in a single query.
func (s *AnnotationStore) ListPublishedItemManifestReferences(ctx context.Context, itemID string) ([]AnnotationManifestReference, error) {
	if s == nil || s.q == nil || strings.TrimSpace(itemID) == "" {
		return nil, fmt.Errorf("list published item annotation manifest references: store and item are required")
	}
	rows, err := s.q.ListPublishedItemAnnotationManifestReferences(ctx, strings.TrimSpace(itemID))
	if err != nil {
		return nil, fmt.Errorf("list published item annotation manifest references: %w", err)
	}
	references := make([]AnnotationManifestReference, 0, len(rows))
	for _, row := range rows {
		reference := AnnotationManifestReference{
			WorkspaceID: row.WorkspaceID,
			ItemImageID: row.ItemImageID,
			PageID:      row.PageID,
			CanvasURI:   row.CanvasUri,
			Revision:    row.PublishedRevision,
			ModifiedAt:  row.PublishedAt,
			Published:   true,
		}
		if err := validateAnnotationManifestReference(reference); err != nil {
			return nil, fmt.Errorf("list published item annotation manifest references: %w", err)
		}
		references = append(references, reference)
	}
	return references, nil
}

func validateAnnotationManifestReference(reference AnnotationManifestReference) error {
	if reference.WorkspaceID == 0 || reference.ItemImageID == 0 || reference.Revision == 0 ||
		strings.TrimSpace(reference.PageID) == "" || strings.TrimSpace(reference.CanvasURI) == "" || reference.ModifiedAt.IsZero() {
		return fmt.Errorf("invalid persisted annotation manifest reference")
	}
	return nil
}

// LoadItemPages returns every canonical page for one item in image order. The
// SQL statement itself enforces the aggregate payload ceiling before sqlc can
// materialize any payloads in memory.
func (s *AnnotationStore) LoadItemPages(ctx context.Context, workspaceID uint64, itemID string, maxSourceBytes int64) ([]AnnotationPage, error) {
	if s == nil || s.q == nil || workspaceID == 0 || strings.TrimSpace(itemID) == "" || maxSourceBytes <= 0 {
		return nil, fmt.Errorf("load item annotation pages: store, workspace, item, and source limit are required")
	}
	rows, err := s.q.ListItemAnnotationPagesManual(ctx, db.ListItemAnnotationPagesManualParams{
		WorkspaceID:    workspaceID,
		ItemID:         itemID,
		MaxSourceBytes: strconv.FormatInt(maxSourceBytes, 10),
	})
	if err != nil {
		return nil, fmt.Errorf("load item annotation pages: %w", err)
	}
	pages := make([]AnnotationPage, 0, len(rows))
	for _, row := range rows {
		page, err := annotationPageFromRow(row, nil)
		if err != nil {
			return nil, fmt.Errorf("load item annotation pages: %w", err)
		}
		pages = append(pages, page)
	}
	return pages, nil
}

// LoadPublishedPage returns the anonymously dereferenceable snapshot for a
// globally unique item image ID. Workspace ownership is intentionally not an
// input: existence in this table is the explicit public-access grant.
func (s *AnnotationStore) LoadPublishedPage(ctx context.Context, itemImageID uint64) (PublishedAnnotationPage, error) {
	if s == nil || s.q == nil || itemImageID == 0 {
		return PublishedAnnotationPage{}, ErrAnnotationPageNotFound
	}
	row, err := s.q.GetPublishedAnnotationPage(ctx, itemImageID)
	return publishedAnnotationPageFromRow(row, err)
}

// ImageURLIsPublished reports whether any explicit annotation publication
// grants anonymous read access to the referenced image bytes. Generated upload
// URLs have immutable per-ingest identities; if callers deliberately share an
// external URL, one published reference intentionally publishes those bytes.
func (s *AnnotationStore) ImageURLIsPublished(ctx context.Context, imageURL string) (bool, error) {
	if s == nil || s.q == nil || strings.TrimSpace(imageURL) == "" {
		return false, nil
	}
	return s.q.PublishedImageURLExists(ctx, strings.TrimSpace(imageURL))
}

// PublishPage snapshots exactly expectedRevision, queues the image graph and
// item-scoped aggregate Manifest projections, and records the publication event atomically. A
// repeated publish of the same revision is idempotent and emits no new event.
func (s *AnnotationStore) PublishPage(
	ctx context.Context,
	workspaceID uint64,
	itemImageID uint64,
	options AnnotationPublicationOptions,
) (PublishedAnnotationPage, error) {
	if s == nil || s.pool == nil {
		return PublishedAnnotationPage{}, fmt.Errorf("publish annotation page: store is not configured")
	}
	if workspaceID == 0 || itemImageID == 0 || options.ExpectedRevision == 0 {
		return PublishedAnnotationPage{}, fmt.Errorf("publish annotation page: workspace, item image, and expected revision are required")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("begin annotation publication: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, workspaceID); err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("publish annotation page: lock storage usage: %w", err)
	}
	queries := s.q.WithTx(tx)
	itemID, err := queries.LockItemImageForUseManual(ctx, db.LockItemImageForUseManualParams{
		ID:          itemImageID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PublishedAnnotationPage{}, ErrAnnotationPageNotFound
		}
		return PublishedAnnotationPage{}, fmt.Errorf("publish annotation page: lock item image: %w", err)
	}
	if options.PublishedByUserID != nil {
		if _, err := queries.LockWorkspaceMemberRole(ctx, workspaceID, *options.PublishedByUserID); err != nil {
			return PublishedAnnotationPage{}, fmt.Errorf("publish annotation page: lock publisher membership: %w", err)
		}
	}
	durableBefore, err := itemImageDurableDatabaseBytes(ctx, queries, workspaceID, itemImageID)
	if err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("publish annotation page: measure prior durable storage: %w", err)
	}
	canonical, err := queries.LockAnnotationPageForPublication(ctx, db.LockAnnotationPageForPublicationParams{
		WorkspaceID: workspaceID,
		ItemImageID: itemImageID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return PublishedAnnotationPage{}, ErrAnnotationPageNotFound
	}
	if err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("lock annotation page for publication: %w", err)
	}
	if canonical.Revision != options.ExpectedRevision {
		return PublishedAnnotationPage{}, ErrAnnotationRevisionConflict
	}
	if err := iiif.ValidateAnnotationPage([]byte(canonical.Payload)); err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("publish annotation page: %w", err)
	}

	existing, existingErr := queries.GetPublishedAnnotationPageForUpdate(ctx, db.GetPublishedAnnotationPageForUpdateParams{
		WorkspaceID: workspaceID,
		ItemImageID: itemImageID,
	})
	existingFound := existingErr == nil
	if existingErr == nil {
		if existing.PublishedRevision == canonical.Revision {
			if err := tx.Commit(); err != nil {
				return PublishedAnnotationPage{}, fmt.Errorf("commit idempotent annotation publication: %w", err)
			}
			return publishedAnnotationPageFromRow(existing, nil)
		}
		if existing.PublishedRevision > canonical.Revision {
			return PublishedAnnotationPage{}, ErrAnnotationRevisionConflict
		}
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return PublishedAnnotationPage{}, fmt.Errorf("load current annotation publication: %w", existingErr)
	}

	publishedBy, err := nullablePublishedUserID(options.PublishedByUserID)
	if err != nil {
		return PublishedAnnotationPage{}, err
	}
	// Match DATETIME(6) so the response row, CloudEvent, and webhook payload
	// describe one byte-for-byte publication timestamp.
	publishedAt := time.Now().UTC().Truncate(time.Microsecond)
	if existingFound {
		updated, err := queries.UpdatePublishedAnnotationPage(ctx, db.UpdatePublishedAnnotationPageParams{
			WorkspaceID:       workspaceID,
			ItemImageID:       itemImageID,
			PageID:            canonical.PageID,
			CanvasUri:         canonical.CanvasUri,
			Payload:           canonical.Payload,
			PublishedRevision: canonical.Revision,
			PublishedByUserID: publishedBy,
			PublishedAt:       publishedAt,
		})
		if err != nil {
			return PublishedAnnotationPage{}, fmt.Errorf("update annotation publication: %w", err)
		}
		if updated != 1 {
			return PublishedAnnotationPage{}, fmt.Errorf("update annotation publication: expected one tenant-scoped row, updated %d", updated)
		}
	} else if err := queries.InsertPublishedAnnotationPage(ctx, db.InsertPublishedAnnotationPageParams{
		WorkspaceID:       workspaceID,
		ItemImageID:       itemImageID,
		PageID:            canonical.PageID,
		CanvasUri:         canonical.CanvasUri,
		Payload:           canonical.Payload,
		PublishedRevision: canonical.Revision,
		PublishedByUserID: publishedBy,
		PublishedAt:       publishedAt,
	}); err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("insert annotation publication: %w", err)
	}
	previousPayload := ""
	if existingFound {
		previousPayload = existing.Payload
	}
	if err := replaceAnnotationMirrorTombstones(ctx, queries, itemImageID, previousPayload, canonical.Payload); err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("queue removed standalone Annotations: %w", err)
	}
	if err := queries.UpsertAnnotationMirrorOutbox(ctx, db.UpsertAnnotationMirrorOutboxParams{
		ItemImageID: itemImageID,
		Revision:    canonical.Revision,
		Payload:     canonical.Payload,
	}); err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("queue published annotation mirror: %w", err)
	}
	if err := queries.UpsertResourceCleanup(ctx, db.UpsertResourceCleanupParams{
		Kind:          db.ResourceCleanupOutboxKindTripletPresentationItem,
		ResourceKey:   itemID,
		WorkspaceID:   workspaceID,
		NextAttemptAt: publishedAt,
	}); err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("queue published item Manifest projection: %w", err)
	}
	if strings.TrimSpace(options.EventID) != "" {
		item, err := queries.GetItemForWorkspace(ctx, itemID, workspaceID)
		if err != nil {
			return PublishedAnnotationPage{}, fmt.Errorf("load annotation publication item: %w", err)
		}
		bodyJSON, err := publicationEventJSON(canonical, publishedAt, options, item)
		if err != nil {
			return PublishedAnnotationPage{}, fmt.Errorf("encode annotation publication event: %w", err)
		}
		eventType := strings.TrimSpace(options.EventType)
		if eventType == "" {
			eventType = "dev.scribe.annotations.published"
		}
		if err := queries.InsertEventOutbox(ctx, options.EventID, eventType, nullableUint64(workspaceID), nullableString(options.Subject), bodyJSON); err != nil {
			return PublishedAnnotationPage{}, fmt.Errorf("insert annotation publication event: %w", err)
		}
		if err := queries.InsertWorkspaceWebhookDeliveries(ctx, options.EventID); err != nil {
			return PublishedAnnotationPage{}, fmt.Errorf("insert annotation publication webhook: %w", err)
		}
	}
	stored, err := queries.GetPublishedAnnotationPageForUpdate(ctx, db.GetPublishedAnnotationPageForUpdateParams{
		WorkspaceID: workspaceID,
		ItemImageID: itemImageID,
	})
	if err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("reload annotation publication: %w", err)
	}
	durableAfter, err := itemImageDurableDatabaseBytes(ctx, queries, workspaceID, itemImageID)
	if err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("publish annotation page: measure durable storage: %w", err)
	}
	if err := applyStorageQuotaUsedDeltaWithLimits(ctx, queries, workspaceID, durableBefore, durableAfter, s.quota); err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("publish annotation page: account durable storage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PublishedAnnotationPage{}, fmt.Errorf("commit annotation publication: %w", err)
	}
	published, err := publishedAnnotationPageFromRow(stored, nil)
	published.NewlyPublished = true
	return published, err
}

// SavePage atomically creates or CAS-replaces a full AnnotationPage and its
// derived annotation index. expectedRevision zero is create-only.
func (s *AnnotationStore) SavePage(ctx context.Context, page AnnotationPage, expectedRevision uint64) (saved AnnotationPage, err error) {
	return s.SavePageWithCorrectionMetric(ctx, page, expectedRevision, nil)
}

// SavePageWithCorrectionMetric persists a page and, when supplied, updates the
// latest OCR baseline metric for the same image in the same transaction.
func (s *AnnotationStore) SavePageWithCorrectionMetric(
	ctx context.Context,
	page AnnotationPage,
	expectedRevision uint64,
	metric *AnnotationCorrectionMetric,
) (saved AnnotationPage, err error) {
	result, err := s.savePage(ctx, page, expectedRevision, annotationPageSaveOptions{metric: metric})
	return result.page, err
}

// SavePageAndCompleteTranscriptionJob commits the canonical page, derived
// index, job terminal state, event outbox record, and webhook deliveries in a
// single database transaction.
func (s *AnnotationStore) SavePageAndCompleteTranscriptionJob(
	ctx context.Context,
	page AnnotationPage,
	expectedRevision uint64,
	completion AnnotationJobCompletion,
) (saved AnnotationPage, err error) {
	fence := completion.TranscriptionAttemptFence
	if err := validateTranscriptionFence(fence); err != nil {
		return AnnotationPage{}, ErrAnnotationJobFence
	}
	if expectedRevision != fence.InputRevision {
		return AnnotationPage{}, ErrAnnotationJobFence
	}
	if run := completion.OCRRun; run != nil {
		if strings.TrimSpace(run.SessionID) == "" || run.ItemImageID == nil || *run.ItemImageID != page.ItemImageID {
			return AnnotationPage{}, fmt.Errorf("complete transcription: OCR provenance does not match the canonical page")
		}
	}
	result, err := s.savePage(ctx, page, expectedRevision, annotationPageSaveOptions{
		fence:      &fence,
		completion: &completion,
	})
	return result.page, err
}

// SavePageAndStartReprocessing atomically replaces a canonical page at the
// revision the provider processed, resets its OCR baseline, supersedes any
// older active transcription job, creates the replacement job, and records
// the event outbox entry. A stale page revision leaves every side effect
// untouched.
func (s *AnnotationStore) SavePageAndStartReprocessing(
	ctx context.Context,
	page AnnotationPage,
	expectedRevision uint64,
	commit AnnotationReprocessCommit,
) (AnnotationReprocessResult, error) {
	if expectedRevision == 0 {
		return AnnotationReprocessResult{}, fmt.Errorf("reprocess annotation page: expected revision is required")
	}
	if err := validateAnnotationReprocessCommit(page, commit); err != nil {
		return AnnotationReprocessResult{}, err
	}
	result, err := s.savePage(ctx, page, expectedRevision, annotationPageSaveOptions{reprocess: &commit})
	if err != nil {
		return AnnotationReprocessResult{}, err
	}
	return AnnotationReprocessResult{
		Page:               result.page,
		TranscriptionJobID: result.transcriptionJobID,
	}, nil
}

func (s *AnnotationStore) savePage(
	ctx context.Context,
	page AnnotationPage,
	expectedRevision uint64,
	options annotationPageSaveOptions,
) (result annotationPageSaveResult, err error) {
	if s == nil || s.pool == nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: store is not configured")
	}
	if page.WorkspaceID == 0 || page.ItemImageID == 0 {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: workspace_id and item_image_id are required")
	}
	page.PageID = strings.TrimSpace(page.PageID)
	page.CanvasURI = strings.TrimSpace(page.CanvasURI)
	page.Payload = strings.TrimSpace(page.Payload)
	if page.PageID == "" || page.CanvasURI == "" || page.Payload == "" {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: page_id, canvas_uri, and payload are required")
	}
	identity, err := iiif.PageIdentityFromPageID(page.PageID, page.CanvasURI)
	if err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: %w", err)
	}
	if identity.ItemImageID != page.ItemImageID {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: page id belongs to item image %d, want %d", identity.ItemImageID, page.ItemImageID)
	}
	if err := iiif.ValidateCanonicalAnnotationPage([]byte(page.Payload), identity); err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: %w", err)
	}
	entries, err := annotationIndexEntries(page)
	if err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: %w", err)
	}
	for _, entry := range entries {
		if entry.CanvasURI != page.CanvasURI {
			return annotationPageSaveResult{}, fmt.Errorf("save annotation page: annotation %q targets canvas %q, want %q", entry.ID, entry.CanvasURI, page.CanvasURI)
		}
	}
	var fenceAttemptNumber int32
	if options.fence != nil {
		fenceAttemptNumber, err = transcriptionAttemptNumberToDB(*options.fence)
		if err != nil {
			return annotationPageSaveResult{}, ErrAnnotationJobFence
		}
	}

	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("begin annotation page transaction: %w", err)
	}
	// Rollback is safe after Commit (it returns sql.ErrTxDone) and guarantees
	// that a future panic cannot leave this reserved connection in a live
	// transaction until garbage collection.
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, page.WorkspaceID); err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: lock storage usage: %w", err)
	}
	queries := s.q.WithTx(tx)
	if page.UpdatedByUserID != nil {
		if _, err := queries.LockWorkspaceMemberRole(ctx, page.WorkspaceID, *page.UpdatedByUserID); err != nil {
			return annotationPageSaveResult{}, fmt.Errorf("save annotation page: lock editor membership: %w", err)
		}
	}
	imageDimensions, err := queries.LockItemImageDimensionsForWorkspaceManual(ctx, db.LockItemImageDimensionsForWorkspaceManualParams{
		ID:          page.ItemImageID,
		WorkspaceID: page.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return annotationPageSaveResult{}, ErrAnnotationPageResource
		}
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: lock image dimensions: %w", err)
	}
	var imageWidth, imageHeight uint32
	if imageDimensions.Width.Valid && imageDimensions.Width.Int32 > 0 {
		imageWidth = uint32(imageDimensions.Width.Int32)
	}
	if imageDimensions.Height.Valid && imageDimensions.Height.Int32 > 0 {
		imageHeight = uint32(imageDimensions.Height.Int32)
	}
	if err := iiif.ValidateAnnotationPageGeometry([]byte(page.Payload), imageWidth, imageHeight); err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: %w", err)
	}
	durableBefore, err := itemImageDurableDatabaseBytes(ctx, queries, page.WorkspaceID, page.ItemImageID)
	if err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: measure prior durable storage: %w", err)
	}
	var lockedActiveJob *db.TranscriptionJob
	var lockedReprocessCommit *AnnotationReprocessCommit
	var lockedReprocessContextSnapshot json.RawMessage
	if options.reprocess != nil {
		workspaceID, workspaceErr := lockTranscriptionAdmissionWorkspace(ctx, queries, page.ItemImageID)
		if workspaceErr != nil {
			return annotationPageSaveResult{}, fmt.Errorf("lock reprocess transcription workspace: %w", workspaceErr)
		}
		if workspaceID != page.WorkspaceID {
			return annotationPageSaveResult{}, ErrAnnotationPageResource
		}
		active, lockErr := queries.LockActiveTranscriptionJobForUpdateManual(ctx, nullableUint64(page.ItemImageID))
		if lockErr == nil {
			lockedActiveJob = &active
		} else if !errors.Is(lockErr, sql.ErrNoRows) {
			return annotationPageSaveResult{}, fmt.Errorf("lock active transcription job before annotation page: %w", lockErr)
		}
		if lockedActiveJob == nil {
			if admissionErr := enforceTranscriptionJobAdmission(ctx, queries, workspaceID, s.admission); admissionErr != nil {
				return annotationPageSaveResult{}, admissionErr
			}
		}
		lockedContext, snapshot, contextErr := lockContextSnapshotForWorkspace(ctx, queries, options.reprocess.Context.ID, page.WorkspaceID)
		if contextErr != nil {
			return annotationPageSaveResult{}, fmt.Errorf("lock reprocess context: %w", contextErr)
		}
		commitCopy := *options.reprocess
		commitCopy.Context = lockedContext
		lockedReprocessCommit = &commitCopy
		lockedReprocessContextSnapshot = snapshot
	}
	if options.fence != nil {
		if _, fenceErr := queries.LockActiveTranscriptionJobLeaseManual(ctx, db.LockActiveTranscriptionJobLeaseManualParams{
			ID:            options.fence.JobID,
			AttemptNumber: fenceAttemptNumber,
			InputRevision: options.fence.InputRevision,
			LeaseToken:    nullableString(options.fence.LeaseToken),
		}); fenceErr != nil {
			if errors.Is(fenceErr, sql.ErrNoRows) {
				return annotationPageSaveResult{}, ErrAnnotationJobFence
			}
			return annotationPageSaveResult{}, fmt.Errorf("lock transcription job fence: %w", fenceErr)
		}
		lockedJob, loadErr := queries.GetTranscriptionJobManual(ctx, options.fence.JobID)
		if loadErr != nil {
			return annotationPageSaveResult{}, fmt.Errorf("load fenced transcription job: %w", loadErr)
		}
		if lockedJob.ItemImageID != page.ItemImageID || lockedJob.InputRevision != expectedRevision {
			return annotationPageSaveResult{}, ErrAnnotationJobFence
		}
	}
	resourceExists, err := queries.AnnotationPageResourceExists(ctx, page.WorkspaceID, page.ItemImageID)
	if err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("verify annotation page resource: %w", err)
	}
	if !resourceExists {
		return annotationPageSaveResult{}, ErrAnnotationPageResource
	}

	row, err := annotationPageToRow(page)
	if err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: %w", err)
	}
	if expectedRevision == 0 {
		if createErr := queries.CreateAnnotationPage(ctx, row); createErr != nil {
			if _, loadErr := queries.GetAnnotationPage(ctx, page.WorkspaceID, page.ItemImageID); loadErr == nil {
				return annotationPageSaveResult{}, ErrAnnotationRevisionConflict
			}
			return annotationPageSaveResult{}, fmt.Errorf("create annotation page: %w", createErr)
		}
	} else {
		updated, updateErr := queries.UpdateAnnotationPageCAS(ctx, row, expectedRevision)
		if updateErr != nil {
			return annotationPageSaveResult{}, fmt.Errorf("update annotation page: %w", updateErr)
		}
		if !updated {
			return annotationPageSaveResult{}, ErrAnnotationRevisionConflict
		}
	}

	if err = queries.ReplaceAnnotationIndex(ctx, page.WorkspaceID, page.ItemImageID, indexEntriesToRows(entries)); err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("replace annotation index: %w", err)
	}
	stored, err := queries.GetAnnotationPage(ctx, page.WorkspaceID, page.ItemImageID)
	if err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("reload annotation page: %w", err)
	}
	if options.metric != nil {
		distance, conversionErr := int32FromInt(options.metric.LevenshteinDistance)
		if conversionErr != nil {
			return annotationPageSaveResult{}, fmt.Errorf("save annotation correction metric: %w", conversionErr)
		}
		if metricErr := queries.SaveCanonicalOCRCorrectionMetric(ctx, db.SaveCanonicalOCRCorrectionMetricParams{
			CanonicalRevision:   stored.Revision,
			LevenshteinDistance: distance,
			ItemImageID:         page.ItemImageID,
		}); metricErr != nil {
			return annotationPageSaveResult{}, fmt.Errorf("save annotation correction metric: %w", metricErr)
		}
	}
	if options.reprocess != nil {
		result.transcriptionJobID, err = commitAnnotationReprocess(ctx, queries, page, stored, lockedActiveJob, *lockedReprocessCommit, lockedReprocessContextSnapshot)
		if err != nil {
			return annotationPageSaveResult{}, err
		}
	}
	if options.completion != nil {
		completion := options.completion
		if completion.OCRRun != nil {
			if completion.OCRRun.ItemImageID == nil || *completion.OCRRun.ItemImageID != page.ItemImageID {
				return annotationPageSaveResult{}, fmt.Errorf("complete transcription: OCR provenance belongs to another item image")
			}
			if insertErr := insertCurrentOCRRun(ctx, queries, *completion.OCRRun); insertErr != nil {
				return annotationPageSaveResult{}, fmt.Errorf("complete transcription: save OCR provenance: %w", insertErr)
			}
		}
		if completionErr := finishTranscriptionAttempt(ctx, queries, completion.TranscriptionAttemptFence, TranscriptionAttemptCompleted, "", &stored.Revision); completionErr != nil {
			if errors.Is(completionErr, ErrTranscriptionJobFence) {
				return annotationPageSaveResult{}, ErrAnnotationJobFence
			}
			return annotationPageSaveResult{}, fmt.Errorf("complete transcription attempt: %w", completionErr)
		}
		completionResult, completionErr := queries.CompleteTranscriptionJobLeasedManual(ctx, db.CompleteTranscriptionJobLeasedManualParams{
			ID:            completion.JobID,
			AttemptNumber: fenceAttemptNumber,
			InputRevision: completion.InputRevision,
			LeaseToken:    nullableString(completion.LeaseToken),
		})
		if completionErr := requireFenceAffected(completionResult, completionErr); completionErr != nil {
			if errors.Is(completionErr, ErrTranscriptionJobFence) {
				return annotationPageSaveResult{}, ErrAnnotationJobFence
			}
			return annotationPageSaveResult{}, fmt.Errorf("complete transcription job: %w", completionErr)
		}
		if strings.TrimSpace(completion.EventID) != "" && strings.TrimSpace(completion.BodyJSON) != "" {
			if eventErr := queries.InsertEventOutbox(ctx, completion.EventID, completion.EventType, nullableUint64(page.WorkspaceID), nullableString(completion.Subject), completion.BodyJSON); eventErr != nil {
				return annotationPageSaveResult{}, fmt.Errorf("insert transcription completion event: %w", eventErr)
			}
			if deliveryErr := queries.InsertWorkspaceWebhookDeliveries(ctx, completion.EventID); deliveryErr != nil {
				return annotationPageSaveResult{}, fmt.Errorf("insert transcription webhook delivery: %w", deliveryErr)
			}
		}
	}
	durableAfter, err := itemImageDurableDatabaseBytes(ctx, queries, page.WorkspaceID, page.ItemImageID)
	if err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: measure durable storage: %w", err)
	}
	if err := applyStorageQuotaUsedDeltaWithLimits(ctx, queries, page.WorkspaceID, durableBefore, durableAfter, s.quota); err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("save annotation page: account durable storage: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return annotationPageSaveResult{}, fmt.Errorf("commit annotation page: %w", err)
	}
	result.page, err = annotationPageFromRow(stored, nil)
	return result, err
}

func validateAnnotationReprocessCommit(page AnnotationPage, commit AnnotationReprocessCommit) error {
	if commit.Context.ID == 0 || commit.Context.ID > math.MaxInt64 {
		return fmt.Errorf("reprocess annotation page: valid context is required")
	}
	if commit.Context.WorkspaceID != nil && *commit.Context.WorkspaceID != page.WorkspaceID {
		return fmt.Errorf("reprocess annotation page: context belongs to another workspace")
	}
	if strings.TrimSpace(commit.OCRRun.SessionID) == "" || strings.TrimSpace(commit.OCRRun.ImageURL) == "" {
		return fmt.Errorf("reprocess annotation page: OCR session and image URL are required")
	}
	if commit.OCRRun.ItemImageID == nil || *commit.OCRRun.ItemImageID != page.ItemImageID {
		return fmt.Errorf("reprocess annotation page: OCR baseline belongs to another item image")
	}
	if commit.OCRRun.ContextID == nil || *commit.OCRRun.ContextID != commit.Context.ID {
		return fmt.Errorf("reprocess annotation page: OCR baseline context does not match the job context")
	}
	if (strings.TrimSpace(commit.EventID) == "") != (strings.TrimSpace(commit.BodyJSON) == "") {
		return fmt.Errorf("reprocess annotation page: event id and body must be supplied together")
	}
	if commit.ExternalRequest != nil {
		if strings.TrimSpace(commit.ExternalRequest.Source) == "" ||
			strings.TrimSpace(commit.ExternalRequest.IdempotencyKey) == "" ||
			strings.TrimSpace(commit.ExternalRequest.LeaseOwner) == "" {
			return fmt.Errorf("reprocess annotation page: complete external request fence is required")
		}
	}
	return nil
}

func commitAnnotationReprocess(
	ctx context.Context,
	queries *db.Queries,
	page AnnotationPage,
	stored db.AnnotationPage,
	active *db.TranscriptionJob,
	commit AnnotationReprocessCommit,
	contextSnapshot json.RawMessage,
) (uint64, error) {
	if err := insertCurrentOCRRun(ctx, queries, commit.OCRRun); err != nil {
		return 0, fmt.Errorf("commit annotation reprocess: save OCR baseline: %w", err)
	}
	contextID := commit.Context.ID
	if active != nil {
		if active.Status == db.TranscriptionJobsStatusRunning {
			fence, fenceErr := attemptFenceFromRow(*active)
			if fenceErr != nil {
				return 0, fmt.Errorf("commit annotation reprocess: fence prior attempt: %w", fenceErr)
			}
			if finishErr := finishTranscriptionAttempt(ctx, queries, fence, TranscriptionAttemptSuperseded, "superseded by image reprocessing", nil); finishErr != nil {
				return 0, fmt.Errorf("commit annotation reprocess: supersede prior attempt: %w", finishErr)
			}
		}
		updateResult, updateErr := queries.SupersedeTranscriptionJobByIDManual(ctx, active.ID)
		if updateErr := requireOneAffected(updateResult, updateErr); updateErr != nil {
			return 0, fmt.Errorf("commit annotation reprocess: supersede prior job: %w", updateErr)
		}
	}
	jobID, err := queries.CreateTranscriptionJob(ctx, db.CreateTranscriptionJobParams{
		ItemImageID:     page.ItemImageID,
		ContextID:       &contextID,
		ContextSnapshot: contextSnapshot,
	})
	if err != nil {
		return 0, fmt.Errorf("commit annotation reprocess: create transcription job: %w", err)
	}
	if request := commit.ExternalRequest; request != nil {
		if err := queries.CompleteExternalRequest(ctx, db.CompleteExternalRequestManualParams{
			WorkspaceID:        page.WorkspaceID,
			Source:             strings.TrimSpace(request.Source),
			IdempotencyKey:     strings.TrimSpace(request.IdempotencyKey),
			LockedBy:           nullableString(request.LeaseOwner),
			ItemID:             sql.NullString{},
			ItemImageID:        nullableUint64(page.ItemImageID),
			TranscriptionJobID: nullableUint64(jobID),
			SessionID:          nullableString(commit.OCRRun.SessionID),
		}); err != nil {
			return 0, fmt.Errorf("commit annotation reprocess: complete operation reservation: %w", err)
		}
	}
	if strings.TrimSpace(commit.EventID) != "" {
		eventType := strings.TrimSpace(commit.EventType)
		if eventType == "" {
			eventType = "dev.scribe.annotations.reprocessed"
		}
		if err := queries.InsertEventOutbox(ctx, strings.TrimSpace(commit.EventID), eventType, nullableUint64(page.WorkspaceID), nullableString(commit.Subject), commit.BodyJSON); err != nil {
			return 0, fmt.Errorf("commit annotation reprocess: insert event: %w", err)
		}
		if err := queries.InsertWorkspaceWebhookDeliveries(ctx, strings.TrimSpace(commit.EventID)); err != nil {
			return 0, fmt.Errorf("commit annotation reprocess: insert webhook delivery: %w", err)
		}
	}
	if stored.ItemImageID != page.ItemImageID || stored.WorkspaceID != page.WorkspaceID {
		return 0, fmt.Errorf("commit annotation reprocess: stored page identity changed")
	}
	return jobID, nil
}

// SearchIndex returns the ordered query projection for one page.
func (s *AnnotationStore) SearchIndex(ctx context.Context, workspaceID, itemImageID uint64) ([]AnnotationIndexEntry, error) {
	if s == nil || s.q == nil {
		return []AnnotationIndexEntry{}, nil
	}
	rows, err := s.q.SearchAnnotationIndex(ctx, workspaceID, itemImageID)
	if err != nil {
		return nil, fmt.Errorf("search annotation index: %w", err)
	}
	entries := make([]AnnotationIndexEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, annotationIndexEntryFromRow(row))
	}
	return entries, nil
}

// GetIndexEntry returns one annotation projection within a workspace.
func (s *AnnotationStore) GetIndexEntry(ctx context.Context, workspaceID uint64, id string) (AnnotationIndexEntry, error) {
	if s == nil || s.q == nil {
		return AnnotationIndexEntry{}, sql.ErrNoRows
	}
	row, err := s.q.GetAnnotationIndexEntry(ctx, workspaceID, strings.TrimSpace(id))
	if err != nil {
		return AnnotationIndexEntry{}, err
	}
	return annotationIndexEntryFromRow(row), nil
}

func annotationPageFromRow(row db.AnnotationPage, err error) (AnnotationPage, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return AnnotationPage{}, ErrAnnotationPageNotFound
	}
	if err != nil {
		return AnnotationPage{}, err
	}
	page := AnnotationPage{
		WorkspaceID: row.WorkspaceID,
		ItemImageID: row.ItemImageID,
		PageID:      row.PageID,
		CanvasURI:   row.CanvasUri,
		Payload:     row.Payload,
		Revision:    row.Revision,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.UpdatedByUserID.Valid {
		if row.UpdatedByUserID.Int64 < 0 {
			return AnnotationPage{}, fmt.Errorf("updated_by_user_id %d is negative", row.UpdatedByUserID.Int64)
		}
		userID := uint64(row.UpdatedByUserID.Int64)
		page.UpdatedByUserID = &userID
	}
	return page, nil
}

func publishedAnnotationPageFromRow(row db.PublishedAnnotationPage, err error) (PublishedAnnotationPage, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return PublishedAnnotationPage{}, ErrAnnotationPageNotFound
	}
	if err != nil {
		return PublishedAnnotationPage{}, err
	}
	page := PublishedAnnotationPage{
		WorkspaceID:       row.WorkspaceID,
		ItemImageID:       row.ItemImageID,
		PageID:            row.PageID,
		CanvasURI:         row.CanvasUri,
		Payload:           row.Payload,
		PublishedRevision: row.PublishedRevision,
		PublishedAt:       row.PublishedAt,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
	if row.PublishedByUserID.Valid {
		if row.PublishedByUserID.Int64 < 0 {
			return PublishedAnnotationPage{}, fmt.Errorf("published_by_user_id %d is negative", row.PublishedByUserID.Int64)
		}
		userID := uint64(row.PublishedByUserID.Int64)
		page.PublishedByUserID = &userID
	}
	return page, nil
}

func nullablePublishedUserID(userID *uint64) (sql.NullInt64, error) {
	if userID == nil || *userID == 0 {
		return sql.NullInt64{}, nil
	}
	if *userID > math.MaxInt64 {
		return sql.NullInt64{}, fmt.Errorf("published_by_user_id %d exceeds signed database range", *userID)
	}
	return sql.NullInt64{Int64: int64(*userID), Valid: true}, nil
}

func publicationEventJSON(canonical db.AnnotationPage, publishedAt time.Time, options AnnotationPublicationOptions, item db.Item) (string, error) {
	annotationCount := 0
	var document struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(canonical.Payload), &document); err != nil {
		return "", err
	}
	annotationCount = len(document.Items)
	eventType := strings.TrimSpace(options.EventType)
	if eventType == "" {
		eventType = "dev.scribe.annotations.published"
	}
	metadata := map[string]any{}
	if len(item.Metadata) > 0 {
		if err := json.Unmarshal(item.Metadata, &metadata); err != nil {
			return "", err
		}
	}
	data := map[string]any{
		"workspaceId":       item.WorkspaceID,
		"itemId":            item.ID,
		"itemImageId":       canonical.ItemImageID,
		"canvasUri":         canonical.CanvasUri,
		"revision":          canonical.Revision,
		"metadata":          metadata,
		"annotationCount":   annotationCount,
		"annotationPageId":  canonical.PageID,
		"publishedRevision": canonical.Revision,
		"publicUrl":         canonical.PageID,
		"publishedAt":       publishedAt.Format(time.RFC3339Nano),
	}
	if item.ExternalReferenceID != "" {
		data["externalReferenceId"] = item.ExternalReferenceID
	}
	if item.CallerIdempotencyKey != "" {
		data["idempotencyKey"] = item.CallerIdempotencyKey
	}
	body, err := json.Marshal(map[string]any{
		"specversion":     "1.0",
		"id":              strings.TrimSpace(options.EventID),
		"source":          "/scribe",
		"type":            eventType,
		"subject":         strings.TrimSpace(options.Subject),
		"time":            publishedAt.Format(time.RFC3339Nano),
		"datacontenttype": "application/json",
		"data":            data,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func annotationPageToRow(page AnnotationPage) (db.AnnotationPage, error) {
	updatedByUserID := sql.NullInt64{}
	if page.UpdatedByUserID != nil {
		if *page.UpdatedByUserID > math.MaxInt64 {
			return db.AnnotationPage{}, fmt.Errorf("updated_by_user_id %d exceeds signed database range", *page.UpdatedByUserID)
		}
		updatedByUserID = sql.NullInt64{Int64: int64(*page.UpdatedByUserID), Valid: true}
	}
	return db.AnnotationPage{
		WorkspaceID:     page.WorkspaceID,
		ItemImageID:     page.ItemImageID,
		PageID:          page.PageID,
		CanvasUri:       page.CanvasURI,
		Payload:         page.Payload,
		Revision:        page.Revision,
		UpdatedByUserID: updatedByUserID,
		CreatedAt:       page.CreatedAt,
		UpdatedAt:       page.UpdatedAt,
	}, nil
}

func annotationIndexEntryFromRow(row db.Annotation) AnnotationIndexEntry {
	return AnnotationIndexEntry{
		WorkspaceID:     row.WorkspaceID,
		ItemImageID:     row.ItemImageID,
		ID:              row.ID,
		CanvasURI:       row.CanvasUri,
		TextGranularity: row.TextGranularity.String,
		Position:        row.Position,
		Payload:         row.Payload,
	}
}

func indexEntriesToRows(entries []AnnotationIndexEntry) []db.Annotation {
	rows := make([]db.Annotation, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, db.Annotation{
			WorkspaceID:     entry.WorkspaceID,
			ItemImageID:     entry.ItemImageID,
			ID:              entry.ID,
			CanvasUri:       entry.CanvasURI,
			TextGranularity: sql.NullString{String: entry.TextGranularity, Valid: entry.TextGranularity != ""},
			Position:        entry.Position,
			Payload:         entry.Payload,
		})
	}
	return rows
}

func annotationIndexEntries(page AnnotationPage) ([]AnnotationIndexEntry, error) {
	var document struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(page.Payload), &document); err != nil {
		return nil, fmt.Errorf("invalid annotation page json: %w", err)
	}
	entries := make([]AnnotationIndexEntry, 0, len(document.Items))
	seen := make(map[string]struct{}, len(document.Items))
	for position, raw := range document.Items {
		if position > math.MaxInt32 {
			return nil, fmt.Errorf("annotation page contains too many items to index")
		}
		var annotation map[string]any
		if err := iiif.DecodeJSON(raw, &annotation); err != nil {
			return nil, fmt.Errorf("invalid annotation at position %d: %w", position, err)
		}
		id := stringProperty(annotation, "id")
		if id == "" {
			return nil, fmt.Errorf("annotation at position %d has no id", position)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("duplicate annotation id %q", id)
		}
		seen[id] = struct{}{}
		canvasURI := annotationCanvasURI(annotation)
		if canvasURI == "" {
			canvasURI = page.CanvasURI
		}
		entries = append(entries, AnnotationIndexEntry{
			WorkspaceID:     page.WorkspaceID,
			ItemImageID:     page.ItemImageID,
			ID:              id,
			CanvasURI:       canvasURI,
			TextGranularity: stringProperty(annotation, "textGranularity"),
			Position:        uint32(position),
			Payload:         string(raw),
		})
	}
	return entries, nil
}

func stringProperty(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return strings.TrimSpace(text)
}

func annotationCanvasURI(annotation map[string]any) string {
	return iiif.TargetCanvas(annotation["target"])
}
