package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	optionsv1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1/options/v1"
)

const (
	defaultEditorReviewTokenTTL   = 5 * time.Minute
	minimumEditorReviewTokenTTL   = time.Minute
	maximumEditorReviewTokenTTL   = 10 * time.Minute
	defaultEditorReviewSessionTTL = 2 * time.Hour
	minimumEditorReviewSessionTTL = 5 * time.Minute
	maximumEditorReviewSessionTTL = 8 * time.Hour
	maximumEditorReviewTokenBytes = 2048
)

var errInvalidEditorReviewToken = errors.New("invalid or expired editor review token")

type editorReviewTokenSigner struct {
	key []byte
	now func() time.Time
}

type editorReviewTokenClaims struct {
	Version     uint32 `json:"v"`
	ID          string `json:"jti"`
	WorkspaceID uint64 `json:"wid"`
	ItemID      string `json:"item"`
	ItemImageID uint64 `json:"image"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
}

func newEditorReviewTokenSigner(secret string) *editorReviewTokenSigner {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil
	}
	digest := sha256.Sum256([]byte("scribe/editor-review-token/v1\x00" + secret))
	return &editorReviewTokenSigner{key: digest[:], now: time.Now}
}

func (s *editorReviewTokenSigner) issue(workspaceID uint64, itemID string, itemImageID uint64, ttl time.Duration) (string, editorReviewTokenClaims, error) {
	if s == nil || len(s.key) == 0 || workspaceID == 0 || itemImageID == 0 || strings.TrimSpace(itemID) == "" || ttl < minimumEditorReviewTokenTTL || ttl > maximumEditorReviewTokenTTL {
		return "", editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	now := s.now().UTC()
	claims := editorReviewTokenClaims{
		Version:     1,
		ID:          uuid.NewString(),
		WorkspaceID: workspaceID,
		ItemID:      strings.TrimSpace(itemID),
		ItemImageID: itemImageID,
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", editorReviewTokenClaims{}, fmt.Errorf("encode editor review token: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	token := encoded + "." + base64.RawURLEncoding.EncodeToString(s.sign(encoded))
	if len(token) > maximumEditorReviewTokenBytes {
		return "", editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	return token, claims, nil
}

func (s *editorReviewTokenSigner) consume(raw string) (editorReviewTokenClaims, error) {
	if s == nil || len(s.key) == 0 {
		return editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maximumEditorReviewTokenBytes {
		return editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, s.sign(parts[0])) {
		return editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) == 0 || len(payload) > maximumEditorReviewTokenBytes {
		return editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims editorReviewTokenClaims
	if err := decoder.Decode(&claims); err != nil {
		return editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	issuedAt := time.Unix(claims.IssuedAt, 0).UTC()
	expiresAt := time.Unix(claims.ExpiresAt, 0).UTC()
	now := s.now().UTC()
	if claims.Version != 1 || claims.ID == "" || claims.WorkspaceID == 0 || claims.ItemImageID == 0 || strings.TrimSpace(claims.ItemID) == "" ||
		issuedAt.After(now.Add(time.Minute)) || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maximumEditorReviewTokenTTL || !now.Before(expiresAt) {
		return editorReviewTokenClaims{}, errInvalidEditorReviewToken
	}
	return claims, nil
}

func (s *editorReviewTokenSigner) sign(encodedPayload string) []byte {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(encodedPayload))
	return mac.Sum(nil)
}

func editorReviewTokenHash(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func editorReviewerSubjectHash(issuer, subject string) string {
	digest := sha256.Sum256([]byte("scribe/editor-review-subject/v1\x00" + strings.TrimSpace(issuer) + "\x00" + strings.TrimSpace(subject)))
	return hex.EncodeToString(digest[:])
}

func (m *Manager) CreateEditorReviewToken(ctx context.Context, req *connect.Request[scribev1.CreateEditorReviewTokenRequest]) (*connect.Response[scribev1.CreateEditorReviewTokenResponse], error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok || principal.AuthType != "external_jwt" || strings.TrimSpace(principal.ExternalIssuer) == "" {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("registered external JWT authentication is required"))
	}
	if m.reviewTokens == nil || m.identities == nil || m.items == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("editor review handoff is not configured"))
	}
	subject := strings.TrimSpace(req.Msg.GetReviewerSubject())
	name := strings.TrimSpace(req.Msg.GetReviewerName())
	email, err := normalizeReviewerEmail(req.Msg.GetReviewerEmail())
	if err != nil || !boundedReviewIdentity(subject, 255) || !boundedReviewIdentity(name, 255) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reviewer identity is invalid"))
	}
	image, err := m.items.GetImageForWorkspace(ctx, req.Msg.GetItemImageId(), principal.WorkspaceID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item image not found"))
	}
	tokenTTL, err := boundedReviewDuration(req.Msg.GetTokenTtlSeconds(), defaultEditorReviewTokenTTL, minimumEditorReviewTokenTTL, maximumEditorReviewTokenTTL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token_ttl_seconds is outside the supported range"))
	}
	sessionTTL, err := boundedReviewDuration(req.Msg.GetSessionTtlSeconds(), defaultEditorReviewSessionTTL, minimumEditorReviewSessionTTL, maximumEditorReviewSessionTTL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("session_ttl_seconds is outside the supported range"))
	}
	rawToken, claims, err := m.reviewTokens.issue(principal.WorkspaceID, image.ItemID, image.ID, tokenTTL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create editor review token"))
	}
	if err := m.identities.CreateEditorReviewGrant(ctx, store.EditorReviewGrant{
		ID:                  claims.ID,
		TokenHash:           editorReviewTokenHash(rawToken),
		WorkspaceID:         claims.WorkspaceID,
		ItemID:              claims.ItemID,
		ItemImageID:         claims.ItemImageID,
		IssuedByUserID:      principal.UserID,
		ReviewerSubjectHash: editorReviewerSubjectHash(principal.ExternalIssuer, subject),
		ReviewerName:        name,
		ReviewerEmail:       email,
		SessionTTL:          sessionTTL,
		ExpiresAt:           time.Unix(claims.ExpiresAt, 0).UTC(),
	}); err != nil {
		switch {
		case errors.Is(err, store.ErrEditorReviewTokenLimit):
			return nil, connect.NewError(connect.CodeResourceExhausted, err)
		case errors.Is(err, store.ErrEditorReviewTokenInvalid):
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("editor review grant is no longer authorized"))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist editor review token"))
		}
	}
	reviewURL, err := m.editorReviewURL(rawToken)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create editor review URL"))
	}
	return connect.NewResponse(&scribev1.CreateEditorReviewTokenResponse{
		ReviewUrl:   reviewURL,
		ExpiresAt:   time.Unix(claims.ExpiresAt, 0).UTC().Format(time.RFC3339),
		WorkspaceId: claims.WorkspaceID,
		ItemId:      claims.ItemID,
		ItemImageId: claims.ItemImageID,
	}), nil
}

func (m *Manager) handleEditorReviewRedeem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if m.reviewTokens == nil || m.identities == nil {
		http.Error(w, "editor review link is unavailable", http.StatusServiceUnavailable)
		return
	}
	rawToken := strings.TrimSpace(r.URL.Query().Get("token"))
	claims, err := m.reviewTokens.consume(rawToken)
	if err != nil {
		http.Error(w, "editor review link is invalid or expired", http.StatusUnauthorized)
		return
	}
	sessionToken, err := randomString(48)
	if err != nil {
		http.Error(w, "editor review link is unavailable", http.StatusServiceUnavailable)
		return
	}
	session, err := m.identities.RedeemEditorReviewGrant(r.Context(), store.RedeemEditorReviewGrantParams{
		TokenHash:       editorReviewTokenHash(rawToken),
		GrantID:         claims.ID,
		WorkspaceID:     claims.WorkspaceID,
		ItemID:          claims.ItemID,
		ItemImageID:     claims.ItemImageID,
		RawSessionToken: sessionToken,
		UserAgent:       r.UserAgent(),
		IPAddress:       ClientIP(r),
	})
	if err != nil {
		if errors.Is(err, store.ErrEditorReviewTokenInvalid) {
			http.Error(w, "editor review link is invalid or expired", http.StatusUnauthorized)
			return
		}
		http.Error(w, "editor review link is unavailable", http.StatusServiceUnavailable)
		return
	}
	m.setSessionCookieTTL(w, sessionToken, time.Until(session.ExpiresAt))
	params := url.Values{
		"itemImageId":  {strconv.FormatUint(session.ItemImageID, 10)},
		"itemId":       {session.ItemID},
		"workspace_id": {strconv.FormatUint(session.Workspace.ID, 10)},
	}
	http.Redirect(w, r, "/editor?"+params.Encode(), http.StatusSeeOther)
}

func (m *Manager) editorReviewSessionPrincipal(ctx context.Context, rawToken string, requestedWorkspaceID uint64) (Principal, error) {
	session, err := m.identities.GetEditorReviewSession(ctx, rawToken)
	if errors.Is(err, sql.ErrNoRows) {
		return m.anonymousPrincipal(), nil
	}
	if err != nil {
		return Principal{}, err
	}
	if requestedWorkspaceID != 0 && requestedWorkspaceID != session.Workspace.ID {
		return Principal{}, fmt.Errorf("editor review session is not valid for the requested workspace")
	}
	return Principal{
		UserID:             session.IssuedBy.ID,
		Email:              session.ReviewerEmail,
		Name:               session.ReviewerName,
		Authenticated:      true,
		AuthType:           "review_session",
		WorkspaceID:        session.Workspace.ID,
		WorkspaceName:      session.Workspace.Name,
		WorkspaceRole:      leastPrivilegedWorkspaceRole("write", session.IssuerRole),
		DefaultWorkspaceID: session.Workspace.ID,
		Scopes: []string{
			"items:read",
			"items:write",
			"annotations:read",
			"annotations:write",
			"transcription:read",
			"transcription:write",
		},
		ScopedItemID:      session.ItemID,
		ScopedItemImageID: session.ItemImageID,
	}, nil
}

func (m *Manager) authorizeEditorReviewSessionRequest(ctx context.Context, principal Principal, procedure string, rule *optionsv1.AuthzRule, request any) error {
	deny := func() error {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("editor review session is scoped to one item image"))
	}
	switch rule.GetResource() {
	case optionsv1.ResourceType_RESOURCE_TYPE_ITEM_IMAGE:
		resourceID, ok := extractFieldValue(request, rule.GetResourceIdField())
		if !ok || resourceID != strconv.FormatUint(principal.ScopedItemImageID, 10) {
			return deny()
		}
		return nil
	case optionsv1.ResourceType_RESOURCE_TYPE_ITEM:
		// Item-scoped reads can aggregate every image (GetItem and prepared
		// exports). The editor uses GetEditorManifest's item-image boundary, so
		// no item-wide procedure is needed by a review session.
		return deny()
	case optionsv1.ResourceType_RESOURCE_TYPE_TRANSCRIPTION_JOB:
		resourceID, ok := extractFieldValue(request, rule.GetResourceIdField())
		if !ok {
			return deny()
		}
		jobID, err := strconv.ParseUint(resourceID, 10, 64)
		if err != nil || m.jobs == nil {
			return deny()
		}
		job, err := m.jobs.Get(ctx, jobID)
		if err != nil || job.WorkspaceID != principal.WorkspaceID || job.ItemImageID != principal.ScopedItemImageID {
			return deny()
		}
		return nil
	case optionsv1.ResourceType_RESOURCE_TYPE_USER:
		switch procedure {
		case "/scribe.v1.AuthService/GetAuthMe":
			return nil
		case "/scribe.v1.TranscriptionService/ListTranscriptionJobs":
			resourceID, ok := extractFieldValue(request, "item_image_id")
			if !ok || resourceID != strconv.FormatUint(principal.ScopedItemImageID, 10) {
				return deny()
			}
			return nil
		default:
			return deny()
		}
	default:
		return deny()
	}
}

func editorReviewSessionAllowsHTTP(principal Principal, r *http.Request) bool {
	if r == nil || r.URL == nil || principal.ScopedItemImageID == 0 {
		return principal.ScopedItemImageID == 0
	}
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/scribe.v1."):
		// Protobuf resource policy performs the exact item/image/job check after
		// decoding the bounded request body.
		return true
	case path == "/logout" || path == "/auth" || strings.HasPrefix(path, "/auth/"):
		return true
	case path == "/livez" || path == "/readyz" || path == "/healthz":
		return true
	case path == "/v1/events":
		raw := strings.TrimSpace(r.URL.Query().Get("item_image_id"))
		return raw == "" || raw == strconv.FormatUint(principal.ScopedItemImageID, 10)
	case strings.HasPrefix(path, "/static/uploads/"):
		// The source handler compares the exact URL with the scoped image.
		return true
	case strings.HasPrefix(path, "/v1/item-images/") && strings.HasSuffix(path, "/hocr"):
		parts := strings.Split(strings.Trim(path, "/"), "/")
		return len(parts) >= 3 && parts[0] == "v1" && parts[1] == "item-images" && parts[2] == strconv.FormatUint(principal.ScopedItemImageID, 10)
	default:
		return false
	}
}

func (m *Manager) editorReviewURL(rawToken string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(m.publicBaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", fmt.Errorf("public base URL is unavailable")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/auth/review"
	base.RawQuery = url.Values{"token": {rawToken}}.Encode()
	return base.String(), nil
}

func boundedReviewDuration(raw uint32, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	if raw == 0 {
		return fallback, nil
	}
	value := time.Duration(raw) * time.Second
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("duration is outside the supported range")
	}
	return value, nil
}

func boundedReviewIdentity(value string, maxRunes int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func normalizeReviewerEmail(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "", nil
	}
	if !utf8.ValidString(raw) || utf8.RuneCountInString(raw) > 320 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("invalid reviewer email")
	}
	parsed, err := mail.ParseAddress(raw)
	if err != nil || !strings.EqualFold(parsed.Address, raw) {
		return "", fmt.Errorf("invalid reviewer email")
	}
	return raw, nil
}
