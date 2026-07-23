package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestProcessHOCRRejectsLocalUploadAliasingBeforeCreatingReference(t *testing.T) {
	database := openTestDB(t)
	ownerWorkspaceID, ownerUserID := createServerTestWorkspace(t, database)
	attackerWorkspaceID, attackerUserID := createServerTestWorkspace(t, database)
	victimURL := processHOCRTestUploadURL()
	victimImage := createServerTestItemImage(
		t,
		database,
		ownerWorkspaceID,
		ownerUserID,
		"https://source.example/canvas/"+uuid.NewString(),
	)
	if _, err := database.Exec(`UPDATE item_images SET image_url = ? WHERE id = ?`, victimURL, victimImage.ID); err != nil {
		t.Fatalf("set victim upload URL: %v", err)
	}

	itemStore := store.NewItemStore(database)
	handler := &Handler{auth: &auth.Manager{}, items: itemStore}
	attackerCtx := auth.WithPrincipal(context.Background(), auth.Principal{
		UserID:        attackerUserID,
		Authenticated: true,
		WorkspaceID:   attackerWorkspaceID,
		WorkspaceRole: "write",
	})

	_, err := handler.ProcessHOCR(attackerCtx, connect.NewRequest(&scribev1.ProcessHOCRRequest{
		Hocr:           "<html><body></body></html>",
		ImageUrl:       victimURL,
		IdempotencyKey: "reject-cross-workspace-upload-alias",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("ProcessHOCR local upload error = %v/%v, want invalid_argument", connect.CodeOf(err), err)
	}
	owned, lookupErr := itemStore.WorkspaceOwnsImageURL(context.Background(), attackerWorkspaceID, victimURL)
	if lookupErr != nil {
		t.Fatalf("check attacker upload references: %v", lookupErr)
	}
	if owned {
		t.Fatal("ProcessHOCR created an attacker-owned reference to the victim upload")
	}
}

func TestProcessHOCRRequiresExactlyOneImageSource(t *testing.T) {
	t.Parallel()

	_, err := (&Handler{}).ProcessHOCR(context.Background(), connect.NewRequest(&scribev1.ProcessHOCRRequest{
		Hocr:           "<html><body></body></html>",
		ImageUrl:       "https://images.example.test/page.jpg",
		ImageData:      []byte("also-inline"),
		IdempotencyKey: "ambiguous-image-source",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("ambiguous image source error = %v/%v, want invalid_argument", connect.CodeOf(err), err)
	}
}

func TestProcessHOCRImageAuthorizationRejectsAllLocalAliasesAndAllowsExternalURLs(t *testing.T) {
	handler := &Handler{}
	ctx := context.Background()

	if err := handler.authorizeProcessHOCRImageURL(ctx, processHOCRTestUploadURL()); !errors.Is(err, errProcessHOCRLocalUpload) {
		t.Fatalf("local upload error = %v, want %v", err, errProcessHOCRLocalUpload)
	}
	if err := handler.authorizeProcessHOCRImageURL(ctx, "https://images.example.test/static/uploads/external.jpg"); err != nil {
		t.Fatalf("external image URL was rejected: %v", err)
	}
	for _, invalidURL := range []string{
		"javascript:alert(1)",
		"/unowned/image.jpg",
		"http://127.0.0.1/private.jpg",
		"https://user:password@images.example.test/private.jpg",
	} {
		if err := handler.authorizeProcessHOCRImageURL(ctx, invalidURL); !errors.Is(err, errProcessHOCRInvalidImage) {
			t.Fatalf("invalid image URL %q error = %v, want %v", invalidURL, err, errProcessHOCRInvalidImage)
		}
	}
}

func processHOCRTestUploadURL() string {
	digest := sha256.Sum256([]byte(uuid.NewString()))
	return fmt.Sprintf("/static/uploads/%x.jpg", digest)
}
