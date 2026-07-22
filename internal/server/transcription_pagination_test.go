package server

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestTranscriptionJobPageTokenIsWorkspaceAndFilterBound(t *testing.T) {
	t.Parallel()
	codec := testItemPageTokenCodec(t)
	createdAt := time.Date(2026, time.July, 20, 12, 34, 56, 789, time.UTC)
	token, err := codec.encodeTranscriptionJobPage(&store.TranscriptionJobPageCursor{
		CreatedAt: createdAt,
		ID:        42,
	}, 7, 9)
	if err != nil {
		t.Fatalf("encode transcription job page token: %v", err)
	}
	cursor, err := codec.decodeTranscriptionJobPage(token, 7, 9)
	if err != nil {
		t.Fatalf("decode transcription job page token: %v", err)
	}
	if cursor.ID != 42 || !cursor.CreatedAt.Equal(createdAt) {
		t.Fatalf("decoded cursor = %+v", cursor)
	}
	if _, err := codec.decodeTranscriptionJobPage(token, 8, 9); err == nil {
		t.Fatal("cross-workspace transcription page token was accepted")
	}
	if _, err := codec.decodeTranscriptionJobPage(token, 7, 10); err == nil {
		t.Fatal("transcription page token was accepted for another item filter")
	}
	if _, err := codec.decode(token, 7, ""); err == nil {
		t.Fatal("transcription token was accepted by item pagination")
	}
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-2] ^= 1
	tampered := base64.RawURLEncoding.EncodeToString(payload) + "." + parts[1]
	if _, err := codec.decodeTranscriptionJobPage(tampered, 7, 9); err == nil {
		t.Fatal("transcription page token with a stale signature was accepted")
	}
}

func TestNormalizeTranscriptionJobPageRequestIsBounded(t *testing.T) {
	t.Parallel()
	codec := testItemPageTokenCodec(t)
	pageSize, cursor, err := normalizeTranscriptionJobPageRequest(0, "", 7, 0, codec)
	if err != nil || pageSize != store.DefaultTranscriptionJobPageSize || cursor != nil {
		t.Fatalf("default job page = %d/%+v/%v", pageSize, cursor, err)
	}
	if _, _, err := normalizeTranscriptionJobPageRequest(store.MaxTranscriptionJobPageSize+1, "", 7, 0, codec); err == nil {
		t.Fatal("oversized transcription job page was accepted")
	}
	if _, _, err := normalizeTranscriptionJobPageRequest(10, " malformed ", 7, 0, codec); err == nil {
		t.Fatal("malformed transcription job page token was accepted")
	}
}
