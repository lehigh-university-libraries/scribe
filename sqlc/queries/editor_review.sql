-- name: CreateEditorReviewTokenManual :exec
INSERT INTO editor_review_tokens (
  id,
  token_hash,
  workspace_id,
  item_id,
  item_image_id,
  issued_by_user_id,
  reviewer_subject_hash,
  reviewer_name,
  reviewer_email,
  session_ttl_seconds,
  expires_at
) VALUES (
  sqlc.arg(id),
  sqlc.arg(token_hash),
  sqlc.arg(workspace_id),
  sqlc.arg(item_id),
  sqlc.arg(item_image_id),
  sqlc.arg(issued_by_user_id),
  sqlc.arg(reviewer_subject_hash),
  sqlc.arg(reviewer_name),
  sqlc.narg(reviewer_email),
  sqlc.arg(session_ttl_seconds),
  sqlc.arg(expires_at)
);

-- name: CountActiveEditorReviewTokensForWorkspaceManual :one
SELECT COUNT(*)
FROM editor_review_tokens
WHERE workspace_id = sqlc.arg(workspace_id)
  AND redeemed_at IS NULL
  AND expires_at > NOW(6);

-- name: LockEditorReviewTokenByHashManual :one
SELECT
  id,
  token_hash,
  workspace_id,
  item_id,
  item_image_id,
  issued_by_user_id,
  reviewer_subject_hash,
  reviewer_name,
  reviewer_email,
  session_ttl_seconds,
  expires_at,
  redeemed_at,
  created_at
FROM editor_review_tokens
WHERE token_hash = sqlc.arg(token_hash)
LIMIT 1
FOR UPDATE;

-- name: MarkEditorReviewTokenRedeemedManual :execresult
UPDATE editor_review_tokens
SET redeemed_at = NOW(6)
WHERE id = sqlc.arg(id)
  AND redeemed_at IS NULL
  AND expires_at > NOW(6);

-- name: CreateEditorReviewSessionManual :exec
INSERT INTO editor_review_sessions (
  token_hash,
  review_token_id,
  workspace_id,
  item_id,
  item_image_id,
  issued_by_user_id,
  reviewer_subject_hash,
  reviewer_name,
  reviewer_email,
  expires_at,
  user_agent,
  ip_address
) VALUES (
  sqlc.arg(token_hash),
  sqlc.arg(review_token_id),
  sqlc.arg(workspace_id),
  sqlc.arg(item_id),
  sqlc.arg(item_image_id),
  sqlc.arg(issued_by_user_id),
  sqlc.arg(reviewer_subject_hash),
  sqlc.arg(reviewer_name),
  sqlc.narg(reviewer_email),
  sqlc.arg(expires_at),
  sqlc.narg(user_agent),
  sqlc.narg(ip_address)
);

-- name: DeleteExpiredEditorReviewSessionsForWorkspaceManual :exec
DELETE FROM editor_review_sessions
WHERE workspace_id = sqlc.arg(workspace_id)
  AND expires_at <= NOW(6);

-- name: CountActiveEditorReviewSessionsForWorkspaceManual :one
SELECT COUNT(*)
FROM editor_review_sessions
WHERE workspace_id = sqlc.arg(workspace_id)
  AND expires_at > NOW(6);

-- name: DeleteOldestEditorReviewSessionForWorkspaceManual :execresult
DELETE FROM editor_review_sessions
WHERE workspace_id = sqlc.arg(workspace_id)
ORDER BY created_at ASC, id ASC
LIMIT 1;

-- name: GetEditorReviewSessionByTokenHashManual :one
SELECT
  id,
  token_hash,
  review_token_id,
  workspace_id,
  item_id,
  item_image_id,
  issued_by_user_id,
  reviewer_subject_hash,
  reviewer_name,
  reviewer_email,
  expires_at,
  user_agent,
  ip_address,
  created_at
FROM editor_review_sessions
WHERE token_hash = sqlc.arg(token_hash)
LIMIT 1;

-- name: DeleteEditorReviewSessionByTokenHashManual :exec
DELETE FROM editor_review_sessions
WHERE token_hash = sqlc.arg(token_hash);

-- name: DeleteExpiredEditorReviewSessionsBatchManual :execresult
DELETE FROM editor_review_sessions
WHERE expires_at <= sqlc.arg(cutoff)
ORDER BY expires_at ASC, id ASC
LIMIT 1000;

-- name: DeleteRetainedEditorReviewTokensBatchManual :execresult
DELETE FROM editor_review_tokens
WHERE expires_at <= DATE_SUB(sqlc.arg(cutoff), INTERVAL 8 HOUR)
ORDER BY expires_at ASC, id ASC
LIMIT 1000;

-- name: DeleteEditorReviewSessionsForItemResourceGraph :exec
DELETE FROM editor_review_sessions
WHERE workspace_id = sqlc.arg(workspace_id)
  AND item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR item_image_id = sqlc.arg(item_image_id));

-- name: DeleteEditorReviewTokensForItemResourceGraph :exec
DELETE FROM editor_review_tokens
WHERE workspace_id = sqlc.arg(workspace_id)
  AND item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR item_image_id = sqlc.arg(item_image_id));
