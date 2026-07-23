package server

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestContextPaginationTokensAreWorkspaceFilterAndDomainBound(t *testing.T) {
	t.Parallel()
	codec := testItemPageTokenCodec(t)
	contextCursor := &store.ContextPageCursor{ID: 42, IsDefault: true, IsSystem: false}
	token, err := codec.encodeContextPage(contextCursor, 7, false)
	if err != nil {
		t.Fatalf("encode context token: %v", err)
	}
	decoded, err := codec.decodeContextPage(token, 7, false)
	if err != nil || *decoded != *contextCursor {
		t.Fatalf("decoded context cursor = %+v/%v, want %+v", decoded, err, contextCursor)
	}
	if _, err := codec.decodeContextPage(token, 8, false); err == nil {
		t.Fatal("context token crossed workspaces")
	}
	if _, err := codec.decodeContextPage(token, 7, true); err == nil {
		t.Fatal("context token crossed system_only filters")
	}
	if _, err := codec.decodeSelectionRulePage(token, 7, 0); err == nil {
		t.Fatal("context token crossed pagination domains")
	}

	ruleCursor := &store.SelectionRulePageCursor{ID: 99, Priority: -5}
	ruleToken, err := codec.encodeSelectionRulePage(ruleCursor, 7, 11)
	if err != nil {
		t.Fatalf("encode rule token: %v", err)
	}
	decodedRule, err := codec.decodeSelectionRulePage(ruleToken, 7, 11)
	if err != nil || *decodedRule != *ruleCursor {
		t.Fatalf("decoded rule cursor = %+v/%v, want %+v", decodedRule, err, ruleCursor)
	}
	if _, err := codec.decodeSelectionRulePage(ruleToken, 7, 12); err == nil {
		t.Fatal("rule token crossed context filters")
	}
}

func TestDecodeContextMetadataJSONRejectsOversizeNonObjectsAndTrailingData(t *testing.T) {
	t.Parallel()
	valid, err := decodeContextMetadataJSON(`{"language":"en","confidence":0.9}`)
	if err != nil || len(valid) != 2 {
		t.Fatalf("bounded metadata = %#v/%v", valid, err)
	}
	for name, raw := range map[string]string{
		"array":    `[]`,
		"null":     `null`,
		"trailing": `{} {}`,
		"oversize": `{"value":"` + strings.Repeat("x", maxContextMetadataJSONBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeContextMetadataJSON(raw); err == nil {
				t.Fatal("invalid metadata was accepted")
			}
		})
	}
}

func TestContextAndRuleHandlersPaginateWithoutTenantLeakage(t *testing.T) {
	database := openTestDB(t)
	ownerUserID := createTestUser(t, database, uniqueName("context-page-owner"))
	t.Cleanup(func() { _, _ = database.Exec(`DELETE FROM users WHERE id = ?`, ownerUserID) })
	ownerWorkspaceID := createTestWorkspace(t, database, ownerUserID, uniqueName("context-page-owner-workspace"))
	otherUserID := createTestUser(t, database, uniqueName("context-page-other"))
	t.Cleanup(func() { _, _ = database.Exec(`DELETE FROM users WHERE id = ?`, otherUserID) })
	otherWorkspaceID := createTestWorkspace(t, database, otherUserID, uniqueName("context-page-other-workspace"))

	contextStore := store.NewContextStore(database)
	createWorkspaceContext := func(workspaceID, userID uint64, name string) store.Context {
		t.Helper()
		created, err := contextStore.Create(context.Background(), store.Context{
			UserID:                &userID,
			WorkspaceID:           &workspaceID,
			Name:                  uniqueName(name),
			SegmentationModel:     "tesseract",
			TranscriptionProvider: "tesseract",
			TranscriptionModel:    "eng",
		})
		if err != nil {
			t.Fatalf("create context: %v", err)
		}
		return created
	}
	ownerContexts := []store.Context{
		createWorkspaceContext(ownerWorkspaceID, ownerUserID, "context-page-a"),
		createWorkspaceContext(ownerWorkspaceID, ownerUserID, "context-page-b"),
		createWorkspaceContext(ownerWorkspaceID, ownerUserID, "context-page-c"),
	}
	foreignContext := createWorkspaceContext(otherWorkspaceID, otherUserID, "context-page-foreign")
	ownerRules := make([]store.ContextSelectionRule, 0, 2)
	for index, contextValue := range ownerContexts[:2] {
		rule, err := contextStore.CreateRuleForWorkspace(context.Background(), ownerWorkspaceID, store.ContextSelectionRule{
			ContextID: contextValue.ID,
			Priority:  int32(10 - index),
			Conditions: []store.RuleCondition{{
				Field: "language", Operator: "eq", Value: "en",
			}},
		})
		if err != nil {
			t.Fatalf("create owner rule: %v", err)
		}
		ownerRules = append(ownerRules, rule)
	}
	foreignRule, err := contextStore.CreateRuleForWorkspace(context.Background(), otherWorkspaceID, store.ContextSelectionRule{
		ContextID: foreignContext.ID,
		Priority:  100,
		Conditions: []store.RuleCondition{{
			Field: "language", Operator: "eq", Value: "en",
		}},
	})
	if err != nil {
		t.Fatalf("create foreign rule: %v", err)
	}

	handler := &Handler{contexts: contextStore, auth: &auth.Manager{}, itemPageTokens: testItemPageTokenCodec(t)}
	requestContext := auth.WithPrincipal(context.Background(), auth.Principal{
		Authenticated: true,
		UserID:        ownerUserID,
		WorkspaceID:   ownerWorkspaceID,
	})

	contextIDs := make(map[uint64]int)
	pageToken := ""
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > 1000 {
			t.Fatal("context pagination did not terminate")
		}
		page, err := handler.ListContexts(requestContext, connect.NewRequest(&scribev1.ListContextsRequest{
			PageSize:  1,
			PageToken: pageToken,
		}))
		if err != nil {
			t.Fatalf("list context page %d: %v", pageNumber, err)
		}
		for _, contextValue := range page.Msg.GetContexts() {
			contextIDs[contextValue.GetId()]++
		}
		pageToken = page.Msg.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	for _, contextValue := range ownerContexts {
		if contextIDs[contextValue.ID] != 1 {
			t.Errorf("owner context %d occurrence count = %d, want 1", contextValue.ID, contextIDs[contextValue.ID])
		}
	}
	if contextIDs[foreignContext.ID] != 0 {
		t.Fatalf("foreign context %d leaked into owner page", foreignContext.ID)
	}

	firstContextPage, err := handler.ListContexts(requestContext, connect.NewRequest(&scribev1.ListContextsRequest{PageSize: 1}))
	if err != nil || firstContextPage.Msg.GetNextPageToken() == "" {
		t.Fatalf("first context page token = %q/%v", firstContextPage.Msg.GetNextPageToken(), err)
	}
	_, err = handler.ListContexts(requestContext, connect.NewRequest(&scribev1.ListContextsRequest{
		SystemOnly: true,
		PageSize:   1,
		PageToken:  firstContextPage.Msg.GetNextPageToken(),
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("changed context filter token code = %s, want invalid_argument", connect.CodeOf(err))
	}

	ruleIDs := make(map[uint64]int)
	pageToken = ""
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > 1000 {
			t.Fatal("selection rule pagination did not terminate")
		}
		page, err := handler.ListSelectionRules(requestContext, connect.NewRequest(&scribev1.ListSelectionRulesRequest{
			PageSize:  1,
			PageToken: pageToken,
		}))
		if err != nil {
			t.Fatalf("list rule page %d: %v", pageNumber, err)
		}
		for _, rule := range page.Msg.GetRules() {
			ruleIDs[rule.GetId()]++
		}
		pageToken = page.Msg.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	for _, rule := range ownerRules {
		if ruleIDs[rule.ID] != 1 {
			t.Errorf("owner rule %d occurrence count = %d, want 1", rule.ID, ruleIDs[rule.ID])
		}
	}
	if ruleIDs[foreignRule.ID] != 0 {
		t.Fatalf("foreign rule %d leaked into owner page", foreignRule.ID)
	}

	firstRulePage, err := handler.ListSelectionRules(requestContext, connect.NewRequest(&scribev1.ListSelectionRulesRequest{PageSize: 1}))
	if err != nil || firstRulePage.Msg.GetNextPageToken() == "" {
		t.Fatalf("first rule page token = %q/%v", firstRulePage.Msg.GetNextPageToken(), err)
	}
	_, err = handler.ListSelectionRules(requestContext, connect.NewRequest(&scribev1.ListSelectionRulesRequest{
		ContextId: ownerContexts[0].ID,
		PageSize:  1,
		PageToken: firstRulePage.Msg.GetNextPageToken(),
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("changed rule filter token code = %s, want invalid_argument", connect.CodeOf(err))
	}
}
