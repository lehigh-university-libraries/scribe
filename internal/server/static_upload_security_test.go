package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/uploadblob"
)

func TestStoredUploadMediaTypeComesFromImageBytes(t *testing.T) {
	t.Parallel()

	var payload bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	if err := png.Encode(&payload, img); err != nil {
		t.Fatal(err)
	}
	payload.WriteString("<script>location='/scribe.v1.ItemService/DeleteItem'</script>")

	if got, ok := storedUploadMediaType(payload.Bytes()); !ok || got != "image/png" {
		t.Fatalf("storedUploadMediaType(valid PNG polyglot) = %q, %t; want image/png, true", got, ok)
	}
	if got, ok := storedUploadMediaType([]byte("<script>alert(document.cookie)</script>")); ok || got != "" {
		t.Fatalf("storedUploadMediaType(HTML) = %q, %t; want rejection", got, ok)
	}
	if got, ok := storedUploadMediaType([]byte{'I', 'I', 42, 0}); !ok || got != "image/tiff" {
		t.Fatalf("storedUploadMediaType(TIFF) = %q, %t", got, ok)
	}
	if got, ok := storedUploadMediaType([]byte{0xff, 0x4f, 0xff, 0x51}); !ok || got != "image/jp2" {
		t.Fatalf("storedUploadMediaType(JPEG 2000) = %q, %t", got, ok)
	}
}

func TestStaticUploadResponsesSetBrowserIsolationHeaders(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/static/uploads/missing.png", nil)
	new(Handler).handleStaticUpload(response, request)

	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := response.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Fatalf("Cross-Origin-Resource-Policy = %q", got)
	}
}

func TestStaticUploadSourceDeclinesByteRanges(t *testing.T) {
	if uploadblob.Enabled() {
		t.Skip("local source-byte test requires SCRIBE_UPLOADS_BUCKET to be unset")
	}
	payload := onePixelPNG(t)
	name := strings.Repeat("b", 64) + "-" + uuid.NewString() + ".png"
	if err := os.MkdirAll("uploads", 0o700); err != nil {
		t.Fatalf("create upload fixture directory: %v", err)
	}
	localPath := filepath.Join("uploads", name)
	if err := os.WriteFile(localPath, payload, 0o600); err != nil {
		t.Fatalf("write upload fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(localPath) })

	ctx := auth.WithPrincipal(context.Background(), auth.Principal{
		Authenticated: true,
		AuthType:      "triplet_source",
	})
	request := httptest.NewRequest(http.MethodGet, staticUploadsPrefix+name, nil).WithContext(ctx)
	request.Header.Set("Range", "bytes=0-0")
	response := httptest.NewRecorder()
	new(Handler).handleStaticUpload(response, request)

	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), payload) {
		t.Fatalf("range source response = %d/%d bytes, want whole-object 200/%d", response.Code, response.Body.Len(), len(payload))
	}
	if got := response.Header().Get("Content-Range"); got != "" {
		t.Fatalf("range source Content-Range = %q, want none", got)
	}
	if got := response.Header().Get("Accept-Ranges"); got != "none" {
		t.Fatalf("range source Accept-Ranges = %q, want none", got)
	}
}

func TestStaticUploadSourceSeparatesMemberDelegatedAndPublishedAccess(t *testing.T) {
	if uploadblob.Enabled() {
		t.Skip("local source-byte integration test requires SCRIBE_UPLOADS_BUCKET to be unset")
	}
	database := openTestDB(t)
	ctx := context.Background()
	selectedWorkspaceID, memberUserID := createServerTestWorkspace(t, database)
	targetWorkspaceID, targetUserID := createServerTestWorkspace(t, database)
	canvasURI := "https://source.example/canvas/" + uuid.NewString()
	image := createServerTestUploadItemImage(t, database, targetWorkspaceID, targetUserID, canvasURI)
	name := strings.Repeat("a", 64) + "-" + uuid.NewString() + ".png"
	imageURL := staticUploadsPrefix + name

	payload := onePixelPNG(t)
	if _, err := database.ExecContext(ctx,
		`UPDATE item_images SET image_url = ?, storage_bytes = ? WHERE workspace_id = ? AND id = ?`,
		imageURL, len(payload), targetWorkspaceID, image.ID,
	); err != nil {
		t.Fatalf("set immutable upload reference: %v", err)
	}
	if err := store.NewItemStore(database).RebuildStorageQuotaUsage(ctx); err != nil {
		t.Fatalf("rebuild fixture storage usage: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'read')`,
		targetWorkspaceID, memberUserID,
	); err != nil {
		t.Fatalf("add target workspace membership: %v", err)
	}

	if err := os.MkdirAll("uploads", 0o700); err != nil {
		t.Fatalf("create upload fixture directory: %v", err)
	}
	localPath := filepath.Join("uploads", name)
	if err := os.WriteFile(localPath, payload, 0o600); err != nil {
		t.Fatalf("write upload fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(localPath) })

	itemStore := store.NewItemStore(database)
	annotationStore := store.NewAnnotationStore(database)
	handler := &Handler{items: itemStore, annotations: annotationStore}

	anonymous := requestStaticUpload(handler, ctx, http.MethodGet, imageURL)
	if anonymous.Code != http.StatusNotFound {
		t.Fatalf("anonymous unpublished status = %d, want 404", anonymous.Code)
	}
	tripletContext := auth.WithPrincipal(ctx, auth.Principal{
		Authenticated: true,
		AuthType:      "triplet_source",
	})
	tripletRead := requestStaticUpload(handler, tripletContext, http.MethodGet, imageURL)
	if tripletRead.Code != http.StatusOK || !bytes.Equal(tripletRead.Body.Bytes(), payload) {
		t.Fatalf("Triplet private source response = %d/%d bytes, want 200/%d", tripletRead.Code, tripletRead.Body.Len(), len(payload))
	}
	assertPrivateUploadSourceHeaders(t, tripletRead)

	memberContext := auth.WithPrincipal(ctx, auth.Principal{
		Authenticated: true,
		AuthType:      "session",
		UserID:        memberUserID,
		WorkspaceID:   selectedWorkspaceID,
		WorkspaceRole: "read",
	})
	member := requestStaticUpload(handler, memberContext, http.MethodGet, imageURL)
	if member.Code != http.StatusOK || !bytes.Equal(member.Body.Bytes(), payload) {
		t.Fatalf("cross-workspace member response = %d/%d bytes, want 200/%d", member.Code, member.Body.Len(), len(payload))
	}
	assertPrivateUploadSourceHeaders(t, member)

	// Triplet's HTTP source probes byte-range support before falling back to a
	// whole-object read. Scribe intentionally declines the range here: hosted
	// uploads are already materialized from object storage, so accepting the
	// probe would turn one image read into a burst of complete object-store
	// reads and eventually surface the admission 429 as a Triplet 500.
	rangeRequest := httptest.NewRequest(http.MethodGet, imageURL, nil).WithContext(memberContext)
	rangeRequest.Header.Set("Range", "bytes=0-0")
	rangeResponse := httptest.NewRecorder()
	handler.handleStaticUpload(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusOK || !bytes.Equal(rangeResponse.Body.Bytes(), payload) {
		t.Fatalf("range source response = %d/%d bytes, want whole-object 200/%d", rangeResponse.Code, rangeResponse.Body.Len(), len(payload))
	}
	if got := rangeResponse.Header().Get("Content-Range"); got != "" {
		t.Fatalf("range source Content-Range = %q, want none", got)
	}
	if got := rangeResponse.Header().Get("Accept-Ranges"); got != "none" {
		t.Fatalf("range source Accept-Ranges = %q, want none", got)
	}

	itemsOnly := auth.WithPrincipal(ctx, auth.Principal{
		Authenticated: true,
		AuthType:      "api_key",
		UserID:        memberUserID,
		WorkspaceID:   selectedWorkspaceID,
		WorkspaceRole: "read",
		Scopes:        []string{"items:read"},
	})
	if response := requestStaticUpload(handler, itemsOnly, http.MethodGet, imageURL); response.Code != http.StatusNotFound {
		t.Fatalf("items-only delegated credential status = %d, want 404", response.Code)
	}

	workspaceScoped := auth.WithPrincipal(ctx, auth.Principal{
		Authenticated: true,
		AuthType:      "api_key",
		UserID:        memberUserID,
		WorkspaceID:   selectedWorkspaceID,
		WorkspaceRole: "read",
		Scopes:        []string{"annotations:read"},
	})
	if response := requestStaticUpload(handler, workspaceScoped, http.MethodGet, imageURL); response.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace API key status = %d, want 404", response.Code)
	}
	workspaceScoped = auth.WithPrincipal(ctx, auth.Principal{
		Authenticated: true,
		AuthType:      "api_key",
		UserID:        targetUserID,
		WorkspaceID:   targetWorkspaceID,
		WorkspaceRole: "read",
		Scopes:        []string{"annotations:read"},
	})
	if response := requestStaticUpload(handler, workspaceScoped, http.MethodHead, imageURL); response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("own-workspace API key HEAD = %d/%d bytes, want 200/0", response.Code, response.Body.Len())
	}

	pageID, err := iiif.CanonicalPageID(handler.publicAnnotationBaseURL(), image.ID)
	if err != nil {
		t.Fatalf("canonical page ID: %v", err)
	}
	annotationID, err := iiif.AnnotationID(pageID, "line-1")
	if err != nil {
		t.Fatalf("annotation ID: %v", err)
	}
	pagePayload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: handler.publicAnnotationBaseURL(),
		ItemImageID:   image.ID,
		CanvasURI:     canvasURI,
	}, []any{mustDecodeAnnotation(canonicalMutationTestAnnotation(annotationID, canvasURI, "published source"))})
	if err != nil {
		t.Fatalf("build canonical page: %v", err)
	}
	saved, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: targetWorkspaceID,
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(pagePayload),
	}, 0)
	if err != nil {
		t.Fatalf("save canonical page: %v", err)
	}
	if _, err := annotationStore.PublishPage(ctx, targetWorkspaceID, image.ID, store.AnnotationPublicationOptions{ExpectedRevision: saved.Revision}); err != nil {
		t.Fatalf("publish canonical page: %v", err)
	}

	// Exercise route registration, anonymous middleware fallback, CORS, and
	// HEAD semantics together. Triplet uses this exact credential-free path for
	// public Image API source reads.
	routed := NewHandler(nil, itemStore, nil, annotationStore, nil, &auth.Manager{}, nil, nil)
	publicHeadRequest := httptest.NewRequest(http.MethodHead, "https://scribe.example"+imageURL, nil)
	publicHeadRequest.Header.Set("Origin", "https://viewer.example")
	publicHead := httptest.NewRecorder()
	routed.ServeHTTP(publicHead, publicHeadRequest)
	if publicHead.Code != http.StatusOK || publicHead.Body.Len() != 0 {
		t.Fatalf("anonymous published HEAD = %d/%d bytes, want 200/0", publicHead.Code, publicHead.Body.Len())
	}
	assertPublicUploadSourceHeaders(t, publicHead)
	if publicHead.Header().Get("Content-Length") == "" {
		t.Fatal("anonymous published HEAD omitted Content-Length")
	}

	publicGet := requestStaticUpload(handler, ctx, http.MethodGet, imageURL)
	if publicGet.Code != http.StatusOK || !bytes.Equal(publicGet.Body.Bytes(), payload) {
		t.Fatalf("anonymous published GET = %d/%d bytes, want 200/%d", publicGet.Code, publicGet.Body.Len(), len(payload))
	}
	assertPublicUploadSourceHeaders(t, publicGet)

	for _, invalid := range []string{
		"/static/uploads/mutable.png",
		imageURL + "?download=1",
		"/static/uploads/../" + name,
	} {
		if response := requestStaticUpload(handler, memberContext, http.MethodGet, invalid); response.Code != http.StatusNotFound {
			t.Errorf("invalid source %q status = %d, want 404", invalid, response.Code)
		}
	}
}

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	var payload bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	if err := png.Encode(&payload, img); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return payload.Bytes()
}

func requestStaticUpload(handler *Handler, ctx context.Context, method, requestURL string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, requestURL, nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.handleStaticUpload(response, request)
	return response
}

func assertPrivateUploadSourceHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("private source headers = cache %q CORP %q", response.Header().Get("Cache-Control"), response.Header().Get("Cross-Origin-Resource-Policy"))
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("private source exposed cross-origin access: %q", response.Header().Get("Access-Control-Allow-Origin"))
	}
	assertUploadSourceVary(t, response)
}

func assertPublicUploadSourceHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Header().Get("Cache-Control") != "public, max-age=60, stale-while-revalidate=300" || response.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Fatalf("public source headers = cache %q CORP %q", response.Header().Get("Cache-Control"), response.Header().Get("Cross-Origin-Resource-Policy"))
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "*" || response.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("public source CORS = origin %q credentials %q", response.Header().Get("Access-Control-Allow-Origin"), response.Header().Get("Access-Control-Allow-Credentials"))
	}
	assertUploadSourceVary(t, response)
}

func assertUploadSourceVary(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	vary := strings.Join(response.Header().Values("Vary"), ",")
	for _, credential := range []string{"Authorization", "Cookie", "X-Scribe-API-Key", "X-Scribe-Workspace-ID"} {
		if !strings.Contains(vary, credential) {
			t.Fatalf("source Vary = %q, want %q", vary, credential)
		}
	}
}
