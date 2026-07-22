package auth

import (
	"strings"
	"testing"

	optionsv1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1/options/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestScribeProceduresHaveAuthorizationCoverage(t *testing.T) {
	t.Parallel()

	var uncovered []string
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if !strings.HasPrefix(string(file.Package()), "scribe.v1") {
			return true
		}
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			service := services.Get(i)
			methods := service.Methods()
			for j := 0; j < methods.Len(); j++ {
				method := methods.Get(j)
				procedure := "/" + string(service.FullName()) + "/" + string(method.Name())
				rule, err := extractAuthzRule(procedure)
				if err == nil && rule != nil && rule.GetAllowAnonymous() {
					continue
				}
				if err == nil && rule != nil && requiredPermissionForProcedure(procedure, rule.GetLevel()) != unmappedProcedurePermission {
					continue
				}
				uncovered = append(uncovered, procedure)
			}
		}
		return true
	})

	if len(uncovered) > 0 {
		t.Fatalf("procedures without authz rule or explicit permission mapping: %s", strings.Join(uncovered, ", "))
	}
}

func TestScribeAuthorizationDescriptorsUseExplicitResources(t *testing.T) {
	t.Parallel()
	var invalid []string
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if !strings.HasPrefix(string(file.Package()), "scribe.v1") {
			return true
		}
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			methods := services.Get(i).Methods()
			for j := 0; j < methods.Len(); j++ {
				method := methods.Get(j)
				procedure := "/" + string(services.Get(i).FullName()) + "/" + string(method.Name())
				rule, err := extractAuthzRule(procedure)
				if err != nil {
					invalid = append(invalid, procedure+": "+err.Error())
					continue
				}
				if rule == nil || rule.GetAllowAnonymous() {
					continue
				}
				if rule.GetResource() == optionsv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
					invalid = append(invalid, procedure+": resource is unspecified")
					continue
				}
				switch rule.GetResource() {
				case optionsv1.ResourceType_RESOURCE_TYPE_USER, optionsv1.ResourceType_RESOURCE_TYPE_SYSTEM:
					continue
				}
				resourceField := strings.TrimSpace(rule.GetResourceIdField())
				if resourceField == "" || !resolvableScalarFieldPath(method.Input(), resourceField) {
					invalid = append(invalid, procedure+": exact resource_id_field is missing or invalid")
				}
			}
		}
		return true
	})
	if len(invalid) > 0 {
		t.Fatalf("invalid authorization descriptors: %s", strings.Join(invalid, ", "))
	}
}

func resolvableScalarFieldPath(message protoreflect.MessageDescriptor, path string) bool {
	parts := strings.Split(path, ".")
	for index, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
		field := message.Fields().ByName(protoreflect.Name(part))
		if field == nil || field.IsList() || field.IsMap() {
			return false
		}
		if index == len(parts)-1 {
			return field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind
		}
		if field.Kind() != protoreflect.MessageKind {
			return false
		}
		message = field.Message()
	}
	return false
}

func TestTenantMutationDescriptorsUseExactResourceIDs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		procedure string
		resource  optionsv1.ResourceType
		field     string
	}{
		{procedure: "/scribe.v1.ItemService/GetUploadBatch", resource: optionsv1.ResourceType_RESOURCE_TYPE_UPLOAD_BATCH, field: "batch_id"},
		{procedure: "/scribe.v1.ItemService/UploadItemImage", resource: optionsv1.ResourceType_RESOURCE_TYPE_UPLOAD_BATCH, field: "batch_id"},
		{procedure: "/scribe.v1.ItemService/CancelUploadBatch", resource: optionsv1.ResourceType_RESOURCE_TYPE_UPLOAD_BATCH, field: "batch_id"},
		{procedure: "/scribe.v1.ContextService/DeleteSelectionRule", resource: optionsv1.ResourceType_RESOURCE_TYPE_SELECTION_RULE, field: "rule_id"},
		{procedure: "/scribe.v1.AnnotationService/SearchAnnotations", resource: optionsv1.ResourceType_RESOURCE_TYPE_ITEM_IMAGE, field: "item_image_id"},
		{procedure: "/scribe.v1.AnnotationService/GetAnnotation", resource: optionsv1.ResourceType_RESOURCE_TYPE_ITEM_IMAGE, field: "item_image_id"},
		{procedure: "/scribe.v1.AnnotationService/EnrichAnnotation", resource: optionsv1.ResourceType_RESOURCE_TYPE_ITEM_IMAGE, field: "item_image_id"},
	} {
		rule, err := extractAuthzRule(test.procedure)
		if err != nil {
			t.Fatalf("extractAuthzRule(%q): %v", test.procedure, err)
		}
		if rule == nil || rule.GetResource() != test.resource || rule.GetResourceIdField() != test.field {
			t.Errorf("%s descriptor = %+v, want resource %s field %q", test.procedure, rule, test.resource, test.field)
		}
	}
}
