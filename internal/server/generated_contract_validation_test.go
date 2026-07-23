package server

import (
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/proto"
)

func TestGeneratedMutationContractsRejectUnboundedAndInvalidInputs(t *testing.T) {
	t.Parallel()
	conditions := make([]*scribev1.RuleCondition, 33)
	for index := range conditions {
		conditions[index] = &scribev1.RuleCondition{Field: "language", Operator: "eq", Value: "en"}
	}
	tests := []struct {
		name    string
		message proto.Message
	}{
		{
			name: "api key name",
			message: &scribev1.CreateAPIKeyRequest{
				Name: strings.Repeat("n", 256), Role: "write",
			},
		},
		{
			name: "api key role",
			message: &scribev1.CreateAPIKeyRequest{
				Name: "automation", Role: "owner",
			},
		},
		{
			name: "duplicate api key scopes",
			message: &scribev1.CreateAPIKeyRequest{
				Name: "automation", Role: "read", Scopes: []string{"items:read", "items:read"},
			},
		},
		{
			name: "provider key minimum",
			message: &scribev1.CreateProviderSecretRequest{
				Provider: "openai", Name: "key", ApiKey: "short", Scope: "workspace",
			},
		},
		{
			name: "provider key bytes",
			message: &scribev1.CreateProviderSecretRequest{
				Provider: "openai", Name: "key", ApiKey: strings.Repeat("k", 8193), Scope: "workspace",
			},
		},
		{
			name: "workspace member email",
			message: &scribev1.AddWorkspaceMemberRequest{
				WorkspaceId: 1, Email: "not-an-email", Role: "read",
			},
		},
		{
			name: "workspace member role",
			message: &scribev1.AddWorkspaceMemberRequest{
				WorkspaceId: 1, Email: "reader@example.org", Role: "owner",
			},
		},
		{
			name: "context prompt",
			message: &scribev1.CreateContextRequest{Context: &scribev1.Context{
				Name: "OCR", SegmentationModel: "layout", TranscriptionProvider: "openai",
				SystemPrompt: strings.Repeat("p", 8193),
			}},
		},
		{
			name: "selection rule fanout",
			message: &scribev1.CreateSelectionRuleRequest{Rule: &scribev1.ContextSelectionRule{
				ContextId: 1, Conditions: conditions,
			}},
		},
		{
			name:    "context metadata bytes",
			message: &scribev1.ResolveContextRequest{MetadataJson: strings.Repeat("m", (64<<10)+1)},
		},
		{
			name:    "context page size",
			message: &scribev1.ListContextsRequest{PageSize: 101},
		},
		{
			name:    "context page token",
			message: &scribev1.ListContextsRequest{PageToken: strings.Repeat("t", 513)},
		},
		{
			name:    "selection rule page size",
			message: &scribev1.ListSelectionRulesRequest{PageSize: 101},
		},
		{
			name:    "selection rule page token",
			message: &scribev1.ListSelectionRulesRequest{PageToken: strings.Repeat("t", 513)},
		},
		{
			name:    "job page size",
			message: &scribev1.ListTranscriptionJobsRequest{PageSize: 101},
		},
		{
			name:    "job page token",
			message: &scribev1.ListTranscriptionJobsRequest{PageToken: strings.Repeat("t", 513)},
		},
		{
			name:    "annotation export omitted format",
			message: &scribev1.ExportAnnotationPageRequest{ItemImageId: 1, ExpectedRevision: 1},
		},
		{
			name: "annotation export unknown format",
			message: &scribev1.ExportAnnotationPageRequest{
				ItemImageId: 1, ExpectedRevision: 1, Format: scribev1.AnnotationExportFormat(999),
			},
		},
		{
			name: "item export omitted format",
			message: &scribev1.PrepareItemExportRequest{
				ItemId: "item", ExpectedRevisions: []*scribev1.ItemImageRevision{{ItemImageId: 1, Revision: 1}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := protovalidate.Validate(test.message); err == nil {
				t.Fatalf("generated contract accepted invalid %T", test.message)
			}
		})
	}
}

func TestGeneratedMutationContractsAcceptBoundedInputs(t *testing.T) {
	t.Parallel()
	valid := []proto.Message{
		&scribev1.CreateAPIKeyRequest{Name: "automation", Role: "write", Scopes: []string{"items:read", "annotations:*"}},
		&scribev1.CreateProviderSecretRequest{Provider: "openai", Name: "production", ApiKey: "secret-key", Scope: "workspace"},
		&scribev1.AddWorkspaceMemberRequest{WorkspaceId: 1, Email: "writer@example.org", Role: "write"},
		&scribev1.CreateContextRequest{Context: &scribev1.Context{
			Name: "OCR", SegmentationModel: "layout", TranscriptionProvider: "tesseract",
		}},
		&scribev1.CreateSelectionRuleRequest{Rule: &scribev1.ContextSelectionRule{
			ContextId:  1,
			Conditions: []*scribev1.RuleCondition{{Field: "language", Operator: "eq", Value: "en"}},
		}},
		&scribev1.ListContextsRequest{PageSize: 100, PageToken: strings.Repeat("t", 512)},
		&scribev1.ListSelectionRulesRequest{PageSize: 100, PageToken: strings.Repeat("t", 512)},
		&scribev1.ListTranscriptionJobsRequest{PageSize: 100, PageToken: strings.Repeat("t", 512)},
		&scribev1.ExportAnnotationPageRequest{
			ItemImageId: 1, ExpectedRevision: 1, Format: scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_HOCR,
		},
		&scribev1.PrepareItemExportRequest{
			ItemId: "item", Format: scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT,
			ExpectedRevisions: []*scribev1.ItemImageRevision{{ItemImageId: 1, Revision: 1}},
		},
	}
	for _, message := range valid {
		if err := protovalidate.Validate(message); err != nil {
			t.Errorf("generated contract rejected bounded %T: %v", message, err)
		}
	}
}
