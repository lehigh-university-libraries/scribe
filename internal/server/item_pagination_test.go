package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestItemPageTokenRoundTripIsWorkspaceBound(t *testing.T) {
	t.Parallel()
	codec := testItemPageTokenCodec(t)
	createdAt := time.Date(2026, time.July, 20, 12, 34, 56, 123, time.UTC)
	token, err := codec.encode(&store.ItemPageCursor{CreatedAt: createdAt, ID: "item-42"}, 7, "marginalia")
	if err != nil {
		t.Fatalf("encodeItemPageToken: %v", err)
	}
	cursor, err := codec.decode(token, 7, "marginalia")
	if err != nil {
		t.Fatalf("decodeItemPageToken: %v", err)
	}
	if !cursor.CreatedAt.Equal(createdAt) || cursor.ID != "item-42" {
		t.Fatalf("decoded cursor = %+v, want %s/item-42", cursor, createdAt)
	}
	if _, err := codec.decode(token, 8, "marginalia"); err == nil {
		t.Fatal("cross-workspace page token was accepted")
	}
	if _, err := codec.decode(token, 7, "different query"); err == nil {
		t.Fatal("page token was accepted with a different query")
	}
	otherCodec, err := newItemPageTokenCodec(strings.Repeat("z", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherCodec.decode(token, 7, "marginalia"); err == nil {
		t.Fatal("page token was accepted with a different signing key")
	}

	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	fields["w"] = float64(8)
	tamperedPayload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	tampered := base64.RawURLEncoding.EncodeToString(tamperedPayload) + "." + parts[1]
	if _, err := codec.decode(tampered, 8, "marginalia"); err == nil {
		t.Fatal("page token with a rewritten workspace and stale signature was accepted")
	}
}

func TestNormalizeItemPageRequestBoundsAndRejectsMalformedTokens(t *testing.T) {
	t.Parallel()
	codec := testItemPageTokenCodec(t)
	pageSize, query, cursor, err := normalizeItemPageRequest(0, "", 7, "  Search term  ", codec)
	if err != nil || pageSize != store.DefaultItemPageSize || query != "Search term" || cursor != nil {
		t.Fatalf("default page request = %d/%q/%+v/%v", pageSize, query, cursor, err)
	}
	if _, _, _, err := normalizeItemPageRequest(store.MaxItemPageSize+1, "", 7, "", codec); err == nil {
		t.Fatal("oversized page was accepted")
	}

	emptyQueryHash := itemFilterBinding("")
	unknownField := signItemPagePayload(t, codec, []byte(`{"v":1,"w":7,"c":"2026-07-20T00:00:00Z","i":"item-1","q":"`+emptyQueryHash+`","extra":true}`))
	outOfRangeTime := signItemPagePayload(t, codec, []byte(`{"v":1,"w":7,"c":"3000-01-01T00:00:00Z","i":"item-1","q":"`+emptyQueryHash+`"}`))
	for _, token := range []string{"not/base64", " ", " " + unknownField, unknownField, outOfRangeTime} {
		if _, _, _, err := normalizeItemPageRequest(10, token, 7, "", codec); err == nil {
			t.Fatalf("malformed token %q was accepted", token)
		}
	}
	if _, err := newItemPageTokenCodec("short"); err == nil {
		t.Fatal("short signing key was accepted")
	}
	longUnicodeQuery := strings.Repeat("🙂", store.MaxItemFilterRunes)
	token, err := codec.encode(&store.ItemPageCursor{
		CreatedAt: time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC),
		ID:        strings.Repeat("i", 64),
	}, 7, longUnicodeQuery)
	if err != nil || len(token) > 512 {
		t.Fatalf("maximum query token length = %d/%v, want at most 512", len(token), err)
	}
}

func TestListItemsHandlerContinuesWithoutDuplicates(t *testing.T) {
	database := openTestDB(t)
	userID := createTestUser(t, database, uniqueName("item-page-user"))
	t.Cleanup(func() { _, _ = database.Exec("DELETE FROM users WHERE id = ?", userID) })
	workspaceID := createTestWorkspace(t, database, userID, uniqueName("item-page-workspace"))
	t.Cleanup(func() { _, _ = database.Exec("DELETE FROM items WHERE workspace_id = ?", workspaceID) })
	createdAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	itemIDs := []string{uniqueName("item-page-c"), uniqueName("item-page-b"), uniqueName("item-page-a")}
	itemNames := []string{"Needle newest 100%_", "Needle older", "Haystack"}
	for index, itemID := range itemIDs {
		if _, err := database.Exec(`INSERT INTO items
  (id, user_id, workspace_id, name, source_type, created_at, updated_at)
VALUES (?, ?, ?, ?, 'upload', ?, ?)`, itemID, userID, workspaceID, itemNames[index], createdAt, createdAt); err != nil {
			t.Fatalf("insert %s: %v", itemID, err)
		}
	}
	if _, err := database.Exec(`INSERT INTO item_images
  (item_id, workspace_id, sequence, image_url, label)
VALUES (?, ?, 0, ?, 'First page')`, itemIDs[0], workspaceID, "https://images.example/first.jpg"); err != nil {
		t.Fatalf("insert preview image: %v", err)
	}

	handler := &Handler{
		items:          store.NewItemStore(database),
		auth:           &auth.Manager{},
		itemPageTokens: testItemPageTokenCodec(t),
	}
	requestContext := auth.WithPrincipal(context.Background(), auth.Principal{
		Authenticated: true,
		UserID:        userID,
		WorkspaceID:   workspaceID,
	})
	first, err := handler.ListItems(requestContext, connect.NewRequest(&scribev1.ListItemsRequest{PageSize: 2}))
	if err != nil {
		t.Fatalf("ListItems(first): %v", err)
	}
	if got := protoItemIDs(first.Msg.GetItems()); len(got) != 2 || got[0] != itemIDs[0] || got[1] != itemIDs[1] {
		t.Fatalf("first page ids = %v, want %v", got, itemIDs[:2])
	}
	if first.Msg.GetItems()[0].GetImageCount() != 1 || first.Msg.GetItems()[0].GetPreviewImage().GetLabel() != "First page" {
		t.Fatalf("first summary = %+v, want one bounded preview", first.Msg.GetItems()[0])
	}
	if first.Msg.GetNextPageToken() == "" {
		t.Fatal("first page did not return a continuation token")
	}
	second, err := handler.ListItems(requestContext, connect.NewRequest(&scribev1.ListItemsRequest{
		PageSize:  2,
		PageToken: first.Msg.GetNextPageToken(),
	}))
	if err != nil {
		t.Fatalf("ListItems(second): %v", err)
	}
	if got := protoItemIDs(second.Msg.GetItems()); len(got) != 1 || got[0] != itemIDs[2] {
		t.Fatalf("second page ids = %v, want [%s]", got, itemIDs[2])
	}
	if second.Msg.GetNextPageToken() != "" {
		t.Fatalf("last page next token = %q, want empty", second.Msg.GetNextPageToken())
	}

	_, err = handler.ListItems(requestContext, connect.NewRequest(&scribev1.ListItemsRequest{PageToken: "malformed"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("malformed token code = %s, want invalid_argument", connect.CodeOf(err))
	}

	filteredFirst, err := handler.ListItems(requestContext, connect.NewRequest(&scribev1.ListItemsRequest{
		PageSize: 1,
		Query:    "  Needle  ",
	}))
	if err != nil {
		t.Fatalf("ListItems(filtered first): %v", err)
	}
	if got := protoItemIDs(filteredFirst.Msg.GetItems()); len(got) != 1 || got[0] != itemIDs[0] {
		t.Fatalf("filtered first page ids = %v, want [%s]", got, itemIDs[0])
	}
	filteredSecond, err := handler.ListItems(requestContext, connect.NewRequest(&scribev1.ListItemsRequest{
		PageSize:  1,
		PageToken: filteredFirst.Msg.GetNextPageToken(),
		Query:     "Needle",
	}))
	if err != nil {
		t.Fatalf("ListItems(filtered second): %v", err)
	}
	if got := protoItemIDs(filteredSecond.Msg.GetItems()); len(got) != 1 || got[0] != itemIDs[1] {
		t.Fatalf("filtered second page ids = %v, want [%s]", got, itemIDs[1])
	}
	_, err = handler.ListItems(requestContext, connect.NewRequest(&scribev1.ListItemsRequest{
		PageToken: filteredFirst.Msg.GetNextPageToken(),
		Query:     "Haystack",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("changed-query token code = %s, want invalid_argument", connect.CodeOf(err))
	}

	literal, err := handler.ListItems(requestContext, connect.NewRequest(&scribev1.ListItemsRequest{Query: "%_"}))
	if err != nil {
		t.Fatalf("ListItems(literal wildcard): %v", err)
	}
	if got := protoItemIDs(literal.Msg.GetItems()); len(got) != 1 || got[0] != itemIDs[0] {
		t.Fatalf("literal wildcard filter ids = %v, want [%s]", got, itemIDs[0])
	}
}

func TestListItemsHandlerFailsClosedWithoutTokenSigner(t *testing.T) {
	handler := &Handler{}
	_, err := handler.ListItems(context.Background(), connect.NewRequest(&scribev1.ListItemsRequest{}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("missing signer code = %s, want internal", connect.CodeOf(err))
	}
}

func testItemPageTokenCodec(t *testing.T) *itemPageTokenCodec {
	t.Helper()
	codec, err := newItemPageTokenCodec(strings.Repeat("k", minItemPageTokenKeyBytes))
	if err != nil {
		t.Fatalf("newItemPageTokenCodec: %v", err)
	}
	return codec
}

func signItemPagePayload(t *testing.T, codec *itemPageTokenCodec, payload []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, codec.key)
	if _, err := mac.Write(payload); err != nil {
		t.Fatalf("sign payload: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func protoItemIDs(items []*scribev1.ItemSummary) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.GetId())
	}
	return ids
}
