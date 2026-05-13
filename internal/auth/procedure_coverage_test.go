package auth

import (
	"strings"
	"testing"

	optionsv1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1/options/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
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
				if methodHasAuthzRule(method) || requiredPermissionForProcedure(procedure, optionsv1.AccessLevel_ACCESS_LEVEL_READ) != "" || procedureExplicitlyDenied(procedure) {
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

func methodHasAuthzRule(method protoreflect.MethodDescriptor) bool {
	options, ok := method.Options().(*descriptorpb.MethodOptions)
	return ok && proto.HasExtension(options, optionsv1.E_Authz)
}

func procedureExplicitlyDenied(procedure string) bool {
	switch procedure {
	case "/scribe.v1.WorkspaceService/CreateWorkspace":
		return true
	default:
		return false
	}
}
