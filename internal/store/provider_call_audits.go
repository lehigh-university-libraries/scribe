package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type ProviderCallAudit struct {
	ID           uint64    `json:"id"`
	SessionID    string    `json:"session_id,omitempty"`
	ItemImageID  *uint64   `json:"item_image_id,omitempty"`
	ContextID    *uint64   `json:"context_id,omitempty"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	Operation    string    `json:"operation"`
	Prompt       string    `json:"prompt,omitempty"`
	RequestJSON  string    `json:"request_json,omitempty"`
	ResponseJSON string    `json:"response_json,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	HTTPStatus   *int      `json:"http_status,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type ItemProviderCallAudit struct {
	ProviderCallAudit
	ItemID            string `json:"item_id"`
	ItemImageSequence uint32 `json:"item_image_sequence,omitempty"`
	ItemImageLabel    string `json:"item_image_label,omitempty"`
}

type ProviderCallAuditStore struct {
	pool *sql.DB
}

func NewProviderCallAuditStore(pool *sql.DB) *ProviderCallAuditStore {
	return &ProviderCallAuditStore{pool: pool}
}

func (s *ProviderCallAuditStore) Create(ctx context.Context, audit ProviderCallAudit) error {
	_, err := s.pool.ExecContext(ctx, `
		INSERT INTO provider_call_audits (
			session_id, item_image_id, context_id, provider, model, operation,
			prompt, request_json, response_json, error_message, http_status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		nullString(audit.SessionID),
		uint64ToNullInt64(audit.ItemImageID),
		uint64ToNullInt64(audit.ContextID),
		audit.Provider,
		audit.Model,
		audit.Operation,
		nullString(audit.Prompt),
		nullString(audit.RequestJSON),
		nullString(audit.ResponseJSON),
		nullString(audit.ErrorMessage),
		intToNullInt64(audit.HTTPStatus),
	)
	if err != nil {
		return fmt.Errorf("insert provider call audit: %w", err)
	}
	return nil
}

func (s *ProviderCallAuditStore) ListByItem(ctx context.Context, itemID string, limit int) ([]ItemProviderCallAudit, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.QueryContext(ctx, `
		SELECT
			a.id,
			COALESCE(a.session_id, ''),
			a.item_image_id,
			a.context_id,
			a.provider,
			a.model,
			a.operation,
			COALESCE(a.prompt, ''),
			COALESCE(a.request_json, ''),
			COALESCE(a.response_json, ''),
			COALESCE(a.error_message, ''),
			a.http_status,
			a.created_at,
			i.id,
			COALESCE(ii.sequence, 0),
			COALESCE(ii.label, '')
		FROM provider_call_audits a
		LEFT JOIN item_images ii ON ii.id = a.item_image_id
		LEFT JOIN items i ON i.id = ii.item_id
		WHERE i.id = ?
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT ?
	`, itemID, limit)
	if err != nil {
		return nil, fmt.Errorf("list provider call audits by item: %w", err)
	}
	defer rows.Close()

	audits := make([]ItemProviderCallAudit, 0)
	for rows.Next() {
		var audit ItemProviderCallAudit
		var sessionID sql.NullString
		var itemImageID sql.NullInt64
		var contextID sql.NullInt64
		var prompt sql.NullString
		var requestJSON sql.NullString
		var responseJSON sql.NullString
		var errorMessage sql.NullString
		var httpStatus sql.NullInt64
		var itemImageLabel sql.NullString
		if err := rows.Scan(
			&audit.ID,
			&sessionID,
			&itemImageID,
			&contextID,
			&audit.Provider,
			&audit.Model,
			&audit.Operation,
			&prompt,
			&requestJSON,
			&responseJSON,
			&errorMessage,
			&httpStatus,
			&audit.CreatedAt,
			&audit.ItemID,
			&audit.ItemImageSequence,
			&itemImageLabel,
		); err != nil {
			return nil, fmt.Errorf("scan provider call audit by item: %w", err)
		}
		if sessionID.Valid {
			audit.SessionID = sessionID.String
		}
		if v, ok := uint64PtrFromNullInt64(itemImageID); ok {
			audit.ItemImageID = v
		}
		if v, ok := uint64PtrFromNullInt64(contextID); ok {
			audit.ContextID = v
		}
		if prompt.Valid {
			audit.Prompt = prompt.String
		}
		if requestJSON.Valid {
			audit.RequestJSON = requestJSON.String
		}
		if responseJSON.Valid {
			audit.ResponseJSON = responseJSON.String
		}
		if errorMessage.Valid {
			audit.ErrorMessage = errorMessage.String
		}
		if httpStatus.Valid {
			v := int(httpStatus.Int64)
			audit.HTTPStatus = &v
		}
		if itemImageLabel.Valid {
			audit.ItemImageLabel = itemImageLabel.String
		}
		audits = append(audits, audit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider call audits by item: %w", err)
	}
	return audits, nil
}

func nullString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func intToNullInt64(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}
