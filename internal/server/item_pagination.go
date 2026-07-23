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
	"time"
	"unicode/utf8"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const (
	itemPageTokenVersion       = 1
	minItemPageTokenKeyBytes   = 32
	maxItemPageTokenKeyBytes   = 1024
	itemPageTokenSignatureSize = sha256.Size
)

var maxItemCursorTime = time.Unix(1<<31-1, 0).UTC()

type itemPageToken struct {
	Version     int    `json:"v"`
	WorkspaceID uint64 `json:"w"`
	CreatedAt   string `json:"c"`
	ItemID      string `json:"i"`
	QueryHash   string `json:"q"`
}

type itemPageTokenCodec struct {
	key []byte
}

func newItemPageTokenCodec(rawKey string) (*itemPageTokenCodec, error) {
	key := strings.TrimSpace(rawKey)
	if key != rawKey || len(key) < minItemPageTokenKeyBytes || len(key) > maxItemPageTokenKeyBytes {
		return nil, fmt.Errorf("page token signing key must contain between %d and %d non-whitespace bytes", minItemPageTokenKeyBytes, maxItemPageTokenKeyBytes)
	}
	return &itemPageTokenCodec{key: append([]byte(nil), key...)}, nil
}

func normalizeItemPageRequest(pageSize uint32, token string, workspaceID uint64, query string, codec *itemPageTokenCodec) (uint32, string, *store.ItemPageCursor, error) {
	if pageSize == 0 {
		pageSize = store.DefaultItemPageSize
	}
	if pageSize > store.MaxItemPageSize {
		return 0, "", nil, fmt.Errorf("page_size must not exceed %d", store.MaxItemPageSize)
	}
	query, err := normalizeItemFilter(query)
	if err != nil {
		return 0, "", nil, err
	}
	if token == "" {
		return pageSize, query, nil, nil
	}
	if codec == nil {
		return 0, "", nil, fmt.Errorf("page token signer is not configured")
	}
	cursor, err := codec.decode(token, workspaceID, query)
	if err != nil {
		return 0, "", nil, fmt.Errorf("invalid page_token: %w", err)
	}
	return pageSize, query, cursor, nil
}

func normalizeItemFilter(query string) (string, error) {
	query = strings.TrimSpace(query)
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > store.MaxItemFilterRunes {
		return "", fmt.Errorf("query must contain at most %d valid Unicode characters", store.MaxItemFilterRunes)
	}
	return query, nil
}

func (c *itemPageTokenCodec) encode(cursor *store.ItemPageCursor, workspaceID uint64, query string) (string, error) {
	if cursor == nil {
		return "", nil
	}
	if c == nil || len(c.key) < minItemPageTokenKeyBytes {
		return "", fmt.Errorf("page token signer is not configured")
	}
	if workspaceID == 0 || cursor.CreatedAt.IsZero() || strings.TrimSpace(cursor.ID) == "" || len(cursor.ID) > 64 {
		return "", fmt.Errorf("invalid item page cursor")
	}
	normalizedQuery, err := normalizeItemFilter(query)
	if err != nil || normalizedQuery != query {
		return "", fmt.Errorf("invalid item page query")
	}
	payload, err := json.Marshal(itemPageToken{
		Version:     itemPageTokenVersion,
		WorkspaceID: workspaceID,
		CreatedAt:   cursor.CreatedAt.UTC().Format(time.RFC3339Nano),
		ItemID:      cursor.ID,
		QueryHash:   itemFilterBinding(query),
	})
	if err != nil {
		return "", fmt.Errorf("marshal item page token: %w", err)
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > 512 {
		return "", fmt.Errorf("encoded item page token exceeds contract limit")
	}
	return token, nil
}

func (c *itemPageTokenCodec) decode(token string, workspaceID uint64, expectedQuery string) (*store.ItemPageCursor, error) {
	if c == nil || len(c.key) < minItemPageTokenKeyBytes {
		return nil, fmt.Errorf("page token signer is not configured")
	}
	if workspaceID == 0 {
		return nil, fmt.Errorf("workspace is required")
	}
	if len(token) > 512 || strings.TrimSpace(token) != token {
		return nil, fmt.Errorf("malformed token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != itemPageTokenSignatureSize {
		return nil, fmt.Errorf("decode token signature")
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, fmt.Errorf("token signature is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var parsed itemPageToken
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode token payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("token contains trailing data")
	}
	if parsed.Version != itemPageTokenVersion || parsed.WorkspaceID != workspaceID || parsed.QueryHash != itemFilterBinding(expectedQuery) {
		return nil, fmt.Errorf("token does not belong to this workspace and query")
	}
	if strings.TrimSpace(parsed.ItemID) == "" || len(parsed.ItemID) > 64 {
		return nil, fmt.Errorf("token item id is invalid")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parsed.CreatedAt)
	if err != nil || createdAt.Before(time.Unix(1, 0).UTC()) || createdAt.After(maxItemCursorTime) {
		return nil, fmt.Errorf("token timestamp is invalid")
	}
	return &store.ItemPageCursor{CreatedAt: createdAt.UTC(), ID: parsed.ItemID}, nil
}

func itemFilterBinding(query string) string {
	digest := sha256.Sum256([]byte(query))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
