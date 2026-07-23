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

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const transcriptionJobPageTokenVersion = 1

var transcriptionJobPageTokenDomain = []byte("scribe:transcription-jobs:v1\x00")

type transcriptionJobPageToken struct {
	Version     int    `json:"v"`
	WorkspaceID uint64 `json:"w"`
	ItemImageID uint64 `json:"m"`
	CreatedAt   string `json:"c"`
	JobID       uint64 `json:"j"`
}

func normalizeTranscriptionJobPageRequest(
	pageSize uint32,
	token string,
	workspaceID, itemImageID uint64,
	codec *itemPageTokenCodec,
) (uint32, *store.TranscriptionJobPageCursor, error) {
	if pageSize == 0 {
		pageSize = store.DefaultTranscriptionJobPageSize
	}
	if pageSize > store.MaxTranscriptionJobPageSize {
		return 0, nil, fmt.Errorf("page_size must not exceed %d", store.MaxTranscriptionJobPageSize)
	}
	if token == "" {
		return pageSize, nil, nil
	}
	if codec == nil {
		return 0, nil, fmt.Errorf("page token signer is not configured")
	}
	cursor, err := codec.decodeTranscriptionJobPage(token, workspaceID, itemImageID)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid page_token: %w", err)
	}
	return pageSize, cursor, nil
}

func (c *itemPageTokenCodec) encodeTranscriptionJobPage(
	cursor *store.TranscriptionJobPageCursor,
	workspaceID, itemImageID uint64,
) (string, error) {
	if cursor == nil {
		return "", nil
	}
	if c == nil || len(c.key) < minItemPageTokenKeyBytes {
		return "", fmt.Errorf("page token signer is not configured")
	}
	if workspaceID == 0 || cursor.ID == 0 || cursor.CreatedAt.IsZero() {
		return "", fmt.Errorf("invalid transcription job page cursor")
	}
	payload, err := json.Marshal(transcriptionJobPageToken{
		Version:     transcriptionJobPageTokenVersion,
		WorkspaceID: workspaceID,
		ItemImageID: itemImageID,
		CreatedAt:   cursor.CreatedAt.UTC().Format(time.RFC3339Nano),
		JobID:       cursor.ID,
	})
	if err != nil {
		return "", fmt.Errorf("marshal transcription job page token: %w", err)
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(transcriptionJobPageTokenDomain)
	_, _ = mac.Write(payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > 512 {
		return "", fmt.Errorf("encoded transcription job page token exceeds contract limit")
	}
	return token, nil
}

func (c *itemPageTokenCodec) decodeTranscriptionJobPage(
	token string,
	workspaceID, expectedItemImageID uint64,
) (*store.TranscriptionJobPageCursor, error) {
	if c == nil || len(c.key) < minItemPageTokenKeyBytes {
		return nil, fmt.Errorf("page token signer is not configured")
	}
	if workspaceID == 0 || len(token) > 512 || strings.TrimSpace(token) != token {
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
	_, _ = mac.Write(transcriptionJobPageTokenDomain)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, fmt.Errorf("token signature is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var parsed transcriptionJobPageToken
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode token payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("token contains trailing data")
	}
	if parsed.Version != transcriptionJobPageTokenVersion || parsed.WorkspaceID != workspaceID || parsed.ItemImageID != expectedItemImageID {
		return nil, fmt.Errorf("token does not belong to this workspace and filter")
	}
	if parsed.JobID == 0 {
		return nil, fmt.Errorf("token job id is invalid")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parsed.CreatedAt)
	if err != nil || createdAt.Before(time.Unix(1, 0).UTC()) || createdAt.After(maxItemCursorTime) {
		return nil, fmt.Errorf("token timestamp is invalid")
	}
	return &store.TranscriptionJobPageCursor{CreatedAt: createdAt.UTC(), ID: parsed.JobID}, nil
}
