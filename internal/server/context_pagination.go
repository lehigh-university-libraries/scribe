package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const contextPageTokenVersion = 1

var (
	contextPageTokenDomain       = []byte("scribe:contexts:v1\x00")
	selectionRulePageTokenDomain = []byte("scribe:context-selection-rules:v1\x00")
)

type contextPageToken struct {
	Version     int    `json:"v"`
	WorkspaceID uint64 `json:"w"`
	SystemOnly  bool   `json:"s"`
	IsDefault   bool   `json:"d"`
	IsSystem    bool   `json:"y"`
	ContextID   uint64 `json:"i"`
}

type selectionRulePageToken struct {
	Version     int    `json:"v"`
	WorkspaceID uint64 `json:"w"`
	ContextID   uint64 `json:"c"`
	Priority    int32  `json:"p"`
	RuleID      uint64 `json:"r"`
}

func normalizeContextPageRequest(pageSize uint32, token string, workspaceID uint64, systemOnly bool, codec *itemPageTokenCodec) (uint32, *store.ContextPageCursor, error) {
	if pageSize == 0 {
		pageSize = store.DefaultContextPageSize
	}
	if pageSize > store.MaxContextPageSize {
		return 0, nil, fmt.Errorf("page_size must not exceed %d", store.MaxContextPageSize)
	}
	if token == "" {
		return pageSize, nil, nil
	}
	if codec == nil {
		return 0, nil, fmt.Errorf("page token signer is not configured")
	}
	cursor, err := codec.decodeContextPage(token, workspaceID, systemOnly)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid page_token: %w", err)
	}
	return pageSize, cursor, nil
}

func normalizeSelectionRulePageRequest(pageSize uint32, token string, workspaceID, contextID uint64, codec *itemPageTokenCodec) (uint32, *store.SelectionRulePageCursor, error) {
	if pageSize == 0 {
		pageSize = store.DefaultSelectionRulePageSize
	}
	if pageSize > store.MaxSelectionRulePageSize {
		return 0, nil, fmt.Errorf("page_size must not exceed %d", store.MaxSelectionRulePageSize)
	}
	if token == "" {
		return pageSize, nil, nil
	}
	if codec == nil {
		return 0, nil, fmt.Errorf("page token signer is not configured")
	}
	cursor, err := codec.decodeSelectionRulePage(token, workspaceID, contextID)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid page_token: %w", err)
	}
	return pageSize, cursor, nil
}

func (c *itemPageTokenCodec) encodeContextPage(cursor *store.ContextPageCursor, workspaceID uint64, systemOnly bool) (string, error) {
	if cursor == nil {
		return "", nil
	}
	if workspaceID == 0 || cursor.ID == 0 || (systemOnly && !cursor.IsSystem) {
		return "", fmt.Errorf("invalid context page cursor")
	}
	return c.encodeContextPaginationToken(contextPageTokenDomain, contextPageToken{
		Version:     contextPageTokenVersion,
		WorkspaceID: workspaceID,
		SystemOnly:  systemOnly,
		IsDefault:   cursor.IsDefault,
		IsSystem:    cursor.IsSystem,
		ContextID:   cursor.ID,
	})
}

func (c *itemPageTokenCodec) decodeContextPage(token string, workspaceID uint64, systemOnly bool) (*store.ContextPageCursor, error) {
	var parsed contextPageToken
	if err := c.decodeContextPaginationToken(token, contextPageTokenDomain, &parsed); err != nil {
		return nil, err
	}
	if parsed.Version != contextPageTokenVersion || parsed.WorkspaceID != workspaceID || parsed.SystemOnly != systemOnly {
		return nil, fmt.Errorf("token does not belong to this workspace and filter")
	}
	if parsed.ContextID == 0 || (systemOnly && !parsed.IsSystem) {
		return nil, fmt.Errorf("token context cursor is invalid")
	}
	return &store.ContextPageCursor{ID: parsed.ContextID, IsDefault: parsed.IsDefault, IsSystem: parsed.IsSystem}, nil
}

func (c *itemPageTokenCodec) encodeSelectionRulePage(cursor *store.SelectionRulePageCursor, workspaceID, contextID uint64) (string, error) {
	if cursor == nil {
		return "", nil
	}
	if workspaceID == 0 || cursor.ID == 0 {
		return "", fmt.Errorf("invalid selection rule page cursor")
	}
	return c.encodeContextPaginationToken(selectionRulePageTokenDomain, selectionRulePageToken{
		Version:     contextPageTokenVersion,
		WorkspaceID: workspaceID,
		ContextID:   contextID,
		Priority:    cursor.Priority,
		RuleID:      cursor.ID,
	})
}

func (c *itemPageTokenCodec) decodeSelectionRulePage(token string, workspaceID, contextID uint64) (*store.SelectionRulePageCursor, error) {
	var parsed selectionRulePageToken
	if err := c.decodeContextPaginationToken(token, selectionRulePageTokenDomain, &parsed); err != nil {
		return nil, err
	}
	if parsed.Version != contextPageTokenVersion || parsed.WorkspaceID != workspaceID || parsed.ContextID != contextID {
		return nil, fmt.Errorf("token does not belong to this workspace and context filter")
	}
	if parsed.RuleID == 0 {
		return nil, fmt.Errorf("token rule cursor is invalid")
	}
	return &store.SelectionRulePageCursor{ID: parsed.RuleID, Priority: parsed.Priority}, nil
}

func (c *itemPageTokenCodec) encodeContextPaginationToken(domain []byte, value any) (string, error) {
	if c == nil || len(c.key) < minItemPageTokenKeyBytes {
		return "", fmt.Errorf("page token signer is not configured")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal page token: %w", err)
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(domain)
	_, _ = mac.Write(payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > 512 {
		return "", fmt.Errorf("encoded page token exceeds contract limit")
	}
	return token, nil
}

func (c *itemPageTokenCodec) decodeContextPaginationToken(token string, domain []byte, destination any) error {
	if c == nil || len(c.key) < minItemPageTokenKeyBytes || len(token) > 512 || strings.TrimSpace(token) != token {
		return fmt.Errorf("malformed token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("decode token payload")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != itemPageTokenSignatureSize {
		return fmt.Errorf("decode token signature")
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(domain)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return fmt.Errorf("token signature is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode token payload")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("token contains trailing data")
	}
	return nil
}
