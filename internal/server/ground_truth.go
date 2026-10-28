package server

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"strconv"

	"connectrpc.com/connect/v2"
	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func (h *Handler) MarkGroundTruth(ctx context.Context, req *scribev1.MarkGroundTruthRequest) (*scribev1.MarkGroundTruthResponse, error) {
	image, err := h.itemImageForRequest(ctx, req.GetItemImageId())
	if err != nil {
		return nil, connect.Errorf(connect.CodeNotFound, "item image not found")
	}
	page, err := h.currentAnnotationPage(ctx, image.ID)
	if err != nil {
		return nil, annotationConnectError(err)
	}
	if req.GetMarked() && page.Revision != req.GetExpectedRevision() {
		return nil, connect.Errorf(connect.CodeAborted, "annotations changed; reload before marking ground truth")
	}
	actor := h.currentUserID(ctx)
	if actor > math.MaxInt64 {
		return nil, connect.Errorf(connect.CodeInternal, "invalid actor identity")
	}
	snapshot := store.GroundTruth{WorkspaceID: h.currentWorkspaceID(ctx), ItemImageID: image.ID, Revision: req.GetExpectedRevision(), Payload: page.Payload, MarkedByUserID: sql.NullInt64{Int64: int64(actor), Valid: true}}
	if req.GetMarked() {
		item, err := h.items.GetForWorkspace(ctx, image.ItemID, h.currentWorkspaceID(ctx))
		if err != nil {
			return nil, connect.Errorf(connect.CodeNotFound, "item not found")
		}
		run, err := h.ocrRuns.GetByItemImageID(ctx, image.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.Errorf(connect.CodeFailedPrecondition, "ground truth requires an original OCR baseline")
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, "load OCR baseline failed").WithCause(err)
		}
		original, err := baselinePage(run, int(image.Width), int(image.Height))
		if err != nil {
			return nil, connect.Errorf(connect.CodeFailedPrecondition, "OCR baseline has no usable geometry")
		}
		originalJSON, err := ocrpage.WriteJSON(original)
		if err != nil {
			return nil, connect.Errorf(connect.CodeFailedPrecondition, "OCR baseline has invalid geometry")
		}
		corrected, err := ocrpage.ReadIIIF([]byte(page.Payload), ocrpage.IIIFReadOptions{CanvasWidth: int(image.Width), CanvasHeight: int(image.Height)})
		if err != nil {
			return nil, connect.Errorf(connect.CodeFailedPrecondition, "page has invalid OCR geometry")
		}
		snapshot.DocumentType = "generic"
		if item.DocumentType != "" {
			snapshot.DocumentType = item.DocumentType
		} else if run.ContextID != nil {
			if c, err := h.contexts.GetForWorkspace(ctx, *run.ContextID, h.currentWorkspaceID(ctx)); err == nil {
				snapshot.DocumentType = c.DocumentProfile()
			}
		}
		snapshot.ContextID = sql.NullInt64{}
		if run.ContextID != nil {
			if *run.ContextID > math.MaxInt64 {
				return nil, connect.Errorf(connect.CodeInternal, "invalid baseline context identity")
			}
			snapshot.ContextID = sql.NullInt64{Int64: int64(*run.ContextID), Valid: true}
		}
		snapshot.OriginalPage = string(originalJSON)
		snapshot.ImageUrl = image.ImageURL
		snapshot.Transcript = corrected.Page.PlainText()
	}
	if err := h.annotations.MarkGroundTruth(ctx, snapshot, req.GetMarked()); err != nil {
		return nil, annotationConnectError(err)
	}
	return &scribev1.MarkGroundTruthResponse{Marked: req.GetMarked(), Revision: snapshot.Revision}, nil
}

func (h *Handler) ExportGroundTruth(ctx context.Context, req *scribev1.ExportGroundTruthRequest) (*scribev1.ExportGroundTruthResponse, error) {
	if req.GetContextId() == 0 && req.GetItemId() == "" {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "select a context or item")
	}
	if req.GetContextId() != 0 {
		if _, err := h.contextForRead(ctx, req.GetContextId()); err != nil {
			return nil, connect.Errorf(connect.CodeNotFound, "context not found")
		}
	}
	if req.GetItemId() != "" {
		if _, err := h.itemForRequest(ctx, req.GetItemId()); err != nil {
			return nil, connect.Errorf(connect.CodeNotFound, "item not found")
		}
	}
	pages, err := h.annotations.GroundTruthBundle(ctx, h.currentWorkspaceID(ctx), req.GetContextId(), req.GetItemId())
	if errors.Is(err, store.ErrGroundTruthExportLimit) {
		return nil, connect.NewError(connect.CodeResourceExhausted, err.Error())
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "load ground truth bundle failed").WithCause(err)
	}
	data, err := writeGroundTruthBundle(pages)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "encode ground truth bundle failed").WithCause(err)
	}
	return &scribev1.ExportGroundTruthResponse{Content: data, Filename: "ground-truth.zip"}, nil
}

func writeGroundTruthBundle(pages []store.GroundTruth) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	var manifest bytes.Buffer
	rows := csv.NewWriter(&manifest)
	if err := rows.Write([]string{"ground_truth", "segmentation", "profile", "image", "transcript"}); err != nil {
		return nil, err
	}
	for _, page := range pages {
		prefix := strconv.FormatUint(page.ItemImageID, 10) + "-r" + strconv.FormatUint(page.Revision, 10)
		gt, original, text := prefix+".gt.htr.json", prefix+".htr.json", prefix+".txt"
		baseline, err := ocrpage.ParseFormat([]byte(page.OriginalPage), ocrpage.FormatHTRJSON, ocrpage.ReadOptions{})
		if err != nil {
			return nil, err
		}
		canonical, err := ocrpage.ReadIIIF([]byte(page.Payload), ocrpage.IIIFReadOptions{CanvasWidth: baseline.Width, CanvasHeight: baseline.Height})
		if err != nil {
			return nil, err
		}
		gtJSON, err := ocrpage.WriteJSON(canonical.Page)
		if err != nil {
			return nil, err
		}
		for _, entry := range []struct{ name, content string }{{gt, string(gtJSON)}, {prefix + ".iiif.json", page.Payload}, {original, page.OriginalPage}, {text, page.Transcript}} {
			file, err := archive.Create(entry.name)
			if err != nil {
				return nil, err
			}
			if _, err := file.Write([]byte(entry.content)); err != nil {
				return nil, err
			}
		}
		if err := rows.Write([]string{gt, original, page.DocumentType, page.ImageUrl, text}); err != nil {
			return nil, err
		}
	}
	rows.Flush()
	if err := rows.Error(); err != nil {
		return nil, err
	}
	file, err := archive.Create("pages.csv")
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(manifest.Bytes()); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("close ground truth archive: %w", err)
	}
	return buffer.Bytes(), nil
}
