package server

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestMultiCanvasItemExportsCanonicalPages(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	itemStore := store.NewItemStore(database)
	annotationStore := store.NewAnnotationStore(database)
	itemID := "export-test-" + uuid.NewString()
	item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          itemID,
		UserID:      store.AnonymousUserID,
		WorkspaceID: store.AnonymousWorkspaceID,
		Name:        "Streaming export",
		SourceType:  "manifest",
		SourceURL:   "https://source.example/manifest/" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create export item: %v", err)
	}
	t.Cleanup(func() { _ = itemStore.DeleteForWorkspace(context.Background(), item.ID, store.AnonymousWorkspaceID) })

	expectedRevisions := make([]*scribev1.ItemImageRevision, 0, 2)
	for sequence, text := range []string{"page one", "page two"} {
		canvasURI := fmt.Sprintf("https://source.example/canvas/%s/%d", uuid.NewString(), sequence+1)
		image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
			ItemID: item.ID, Sequence: uint32(sequence + 1),
			ImageURL: "https://images.example.test/page.jpg", CanvasURI: canvasURI,
			Width: 1000, Height: 1200,
		})
		if err != nil {
			t.Fatalf("create export image %d: %v", sequence+1, err)
		}
		pageID, err := iiif.CanonicalPageID("https://scribe.example", image.ID)
		if err != nil {
			t.Fatal(err)
		}
		annotationID, err := iiif.AnnotationID(pageID, fmt.Sprintf("line-%d", sequence+1))
		if err != nil {
			t.Fatal(err)
		}
		payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
			PublicBaseURL: "https://scribe.example", ItemImageID: image.ID, CanvasURI: canvasURI,
		}, []any{transcriptionAnnotation(annotationID, "line", text, canvasURI, models.BBox{X1: 10, Y1: 20, X2: 300, Y2: 60})})
		if err != nil {
			t.Fatalf("build export page %d: %v", sequence+1, err)
		}
		saved, err := annotationStore.SavePage(ctx, store.AnnotationPage{
			WorkspaceID: store.AnonymousWorkspaceID, ItemImageID: image.ID,
			PageID: pageID, CanvasURI: canvasURI, Payload: string(payload),
		}, 0)
		if err != nil {
			t.Fatalf("save export page %d: %v", sequence+1, err)
		}
		expectedRevisions = append(expectedRevisions, &scribev1.ItemImageRevision{ItemImageId: image.ID, Revision: saved.Revision})
	}

	handler := NewHandler(nil, itemStore, nil, annotationStore, nil, nil, nil, nil)
	handler.itemExportTokens = testItemExportTokenCodec(t)
	snapshot, err := handler.GetItem(ctx, connect.NewRequest(&scribev1.GetItemRequest{ItemId: item.ID}))
	if err != nil {
		t.Fatalf("GetItem export snapshot: %v", err)
	}
	if len(snapshot.Msg.GetAnnotationRevisions()) != len(expectedRevisions) {
		t.Fatalf("GetItem revision count = %d, want %d", len(snapshot.Msg.GetAnnotationRevisions()), len(expectedRevisions))
	}
	for index, revision := range snapshot.Msg.GetAnnotationRevisions() {
		if revision.GetItemImageId() != expectedRevisions[index].GetItemImageId() || revision.GetRevision() != expectedRevisions[index].GetRevision() {
			t.Fatalf("GetItem revision %d = %+v, want %+v", index, revision, expectedRevisions[index])
		}
	}
	limitedPages, err := annotationStore.LoadItemPages(ctx, store.AnonymousWorkspaceID, item.ID, 1)
	if err != nil || len(limitedPages) != 0 {
		t.Fatalf("SQL source-byte preflight = %d pages/%v, want no materialized payloads", len(limitedPages), err)
	}
	textResponse := exportItemRequest(t, handler, item.ID, "txt", expectedRevisions)
	if textResponse.Code != 200 || strings.TrimSpace(textResponse.Body.String()) != "page one\n\npage two" {
		t.Fatalf("text item export = %d/%q", textResponse.Code, textResponse.Body.String())
	}
	preparedHeadURL := prepareItemExportURL(t, handler, item.ID, "txt", expectedRevisions)
	appServer := httptest.NewServer(handler)
	defer appServer.Close()
	headResponse, err := http.Head(appServer.URL + preparedHeadURL)
	if err != nil {
		t.Fatalf("HEAD item export: %v", err)
	}
	defer headResponse.Body.Close()
	headBody, err := io.ReadAll(headResponse.Body)
	if err != nil || headResponse.StatusCode != http.StatusOK || len(headBody) != 0 || headResponse.Header.Get("Content-Length") != fmt.Sprintf("%d", textResponse.Body.Len()) {
		t.Fatalf("HEAD item export = %d/%d/%q/%v, want 200/0/%d", headResponse.StatusCode, len(headBody), headResponse.Header.Get("Content-Length"), err, textResponse.Body.Len())
	}
	zipResponse := exportItemRequest(t, handler, item.ID, "hocr", expectedRevisions)
	if zipResponse.Code != 200 || zipResponse.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("archive item export = %d/%q", zipResponse.Code, zipResponse.Header().Get("Content-Type"))
	}
	archive, err := zip.NewReader(bytes.NewReader(zipResponse.Body.Bytes()), int64(zipResponse.Body.Len()))
	if err != nil {
		t.Fatalf("open hOCR archive: %v", err)
	}
	if len(archive.File) != 2 {
		t.Fatalf("archive entries = %d, want 2", len(archive.File))
	}
	for index, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatalf("open archive entry %d: %v", index, err)
		}
		body, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			t.Fatalf("archive entry %d = %q/%v", index, body, readErr)
		}
		for _, word := range [][]string{{"page", "one"}, {"page", "two"}}[index] {
			if !strings.Contains(string(body), ">"+word+"</span>") {
				t.Fatalf("archive entry %d does not contain word %q: %q", index, word, body)
			}
		}
	}

	staleExpected := []*scribev1.ItemImageRevision{
		{ItemImageId: expectedRevisions[0].GetItemImageId(), Revision: expectedRevisions[0].GetRevision() + 1},
		expectedRevisions[1],
	}
	_, err = handler.PrepareItemExport(ctx, connect.NewRequest(&scribev1.PrepareItemExportRequest{
		ItemId: item.ID, Format: scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT, ExpectedRevisions: staleExpected,
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale PrepareItemExport = %v, want aborted", err)
	}

	preparedBeforeSave := prepareItemExportURL(t, handler, item.ID, "txt", expectedRevisions)
	firstPage, err := annotationStore.LoadPage(ctx, store.AnonymousWorkspaceID, expectedRevisions[0].GetItemImageId())
	if err != nil {
		t.Fatalf("load first page before stale-token test: %v", err)
	}
	if _, err := annotationStore.SavePage(ctx, firstPage, firstPage.Revision); err != nil {
		t.Fatalf("advance first page revision: %v", err)
	}
	staleRequest := httptest.NewRequest("GET", preparedBeforeSave, nil)
	staleRequest.SetPathValue("token", strings.TrimPrefix(preparedBeforeSave, "/v1/item-exports/"))
	staleResponse := httptest.NewRecorder()
	handler.handlePreparedItemExport(staleResponse, staleRequest)
	if staleResponse.Code != 409 || strings.Contains(staleResponse.Body.String(), "page one") {
		t.Fatalf("stale prepared export = %d/%q, want 409 without canonical text", staleResponse.Code, staleResponse.Body.String())
	}

	replayRequest := httptest.NewRequest("GET", preparedBeforeSave, nil)
	replayRequest.SetPathValue("token", strings.TrimPrefix(preparedBeforeSave, "/v1/item-exports/"))
	handler.auth = &auth.Manager{}
	replayRequest = replayRequest.WithContext(auth.WithPrincipal(replayRequest.Context(), auth.Principal{
		Authenticated: true, AuthType: "session", UserID: 200, WorkspaceID: 200, WorkspaceRole: "read",
	}))
	replayResponse := httptest.NewRecorder()
	handler.handlePreparedItemExport(replayResponse, replayRequest)
	if replayResponse.Code != 404 || strings.Contains(replayResponse.Body.String(), "page one") {
		t.Fatalf("workspace-replayed prepared export = %d/%q, want 404 without canonical text", replayResponse.Code, replayResponse.Body.String())
	}
}

func prepareItemExportURL(t *testing.T, handler *Handler, itemID, format string, expectedRevisions []*scribev1.ItemImageRevision) string {
	t.Helper()
	response, err := handler.PrepareItemExport(context.Background(), connect.NewRequest(&scribev1.PrepareItemExportRequest{
		ItemId: itemID, Format: testAnnotationExportFormat(t, format), ExpectedRevisions: expectedRevisions,
	}))
	if err != nil {
		t.Fatalf("PrepareItemExport(%s): %v", format, err)
	}
	return response.Msg.GetDownloadUrl()
}

func exportItemRequest(t *testing.T, handler *Handler, itemID, format string, expectedRevisions []*scribev1.ItemImageRevision) *httptest.ResponseRecorder {
	t.Helper()
	return preparedItemExportRequest(t, handler, itemID, format, expectedRevisions, "GET")
}

func preparedItemExportRequest(t *testing.T, handler *Handler, itemID, format string, expectedRevisions []*scribev1.ItemImageRevision, method string) *httptest.ResponseRecorder {
	t.Helper()
	target := prepareItemExportURL(t, handler, itemID, format, expectedRevisions)
	request := httptest.NewRequest(method, target, nil)
	request.SetPathValue("token", strings.TrimPrefix(target, "/v1/item-exports/"))
	response := httptest.NewRecorder()
	handler.handlePreparedItemExport(response, request)
	return response
}
