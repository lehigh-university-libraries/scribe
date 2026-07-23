// Package openapicontract keeps the generated Connect OpenAPI document aligned
// with Scribe's protobuf authorization contract.
package openapicontract

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	optionsv1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1/options/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"gopkg.in/yaml.v3"
)

const (
	SessionCookieScheme = "SessionCookie"
	APIKeyScheme        = "ScribeAPIKey"
	BearerScheme        = "BearerAuth"
)

// Procedure describes the authentication behavior published for one Connect
// RPC. Path is the canonical Connect procedure path.
type Procedure struct {
	Path           string
	AllowAnonymous bool
	SessionOnly    bool
}

// ScribeProcedures derives the public API authentication contract from the
// same protobuf method option used by the runtime authorization interceptor.
func ScribeProcedures(files *protoregistry.Files) ([]Procedure, error) {
	if files == nil {
		return nil, fmt.Errorf("protobuf file registry is required")
	}
	procedures := make([]Procedure, 0)
	var contractErr error
	files.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package() != "scribe.v1" {
			return true
		}
		services := file.Services()
		for serviceIndex := 0; serviceIndex < services.Len(); serviceIndex++ {
			service := services.Get(serviceIndex)
			methods := service.Methods()
			for methodIndex := 0; methodIndex < methods.Len(); methodIndex++ {
				method := methods.Get(methodIndex)
				rule, err := methodAuthzRule(method)
				if err != nil {
					contractErr = err
					return false
				}
				procedure := Procedure{
					Path:           "/" + string(service.FullName()) + "/" + string(method.Name()),
					AllowAnonymous: rule.GetAllowAnonymous(),
					SessionOnly:    rule.GetSessionOnly(),
				}
				if procedure.AllowAnonymous && procedure.SessionOnly {
					contractErr = fmt.Errorf("%s cannot be both anonymous and session-only", procedure.Path)
					return false
				}
				procedures = append(procedures, procedure)
			}
		}
		return contractErr == nil
	})
	if contractErr != nil {
		return nil, contractErr
	}
	if len(procedures) == 0 {
		return nil, fmt.Errorf("no scribe.v1 procedures are registered")
	}
	sort.Slice(procedures, func(left, right int) bool {
		return procedures[left].Path < procedures[right].Path
	})
	for index := 1; index < len(procedures); index++ {
		if procedures[index-1].Path == procedures[index].Path {
			return nil, fmt.Errorf("duplicate procedure %s", procedures[index].Path)
		}
	}
	return procedures, nil
}

func methodAuthzRule(method protoreflect.MethodDescriptor) (*optionsv1.AuthzRule, error) {
	methodOptions, ok := method.Options().(*descriptorpb.MethodOptions)
	if !ok || !proto.HasExtension(methodOptions, optionsv1.E_Authz) {
		return nil, fmt.Errorf("%s has no authz option", method.FullName())
	}
	rule, ok := proto.GetExtension(methodOptions, optionsv1.E_Authz).(*optionsv1.AuthzRule)
	if !ok || rule == nil {
		return nil, fmt.Errorf("%s has an invalid authz option", method.FullName())
	}
	return rule, nil
}

// Rewrite adds stable API metadata and authentication schemes to a generated
// Connect OpenAPI 3.1 document. It fails when any protobuf procedure is absent
// or lacks a generated POST operation, preventing stale or empty path entries.
func Rewrite(source []byte, procedures []Procedure) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, fmt.Errorf("decode generated OpenAPI: %w", err)
	}
	root, err := documentMapping(&document)
	if err != nil {
		return nil, err
	}
	openAPIVersion, ok := mappingValue(root, "openapi")
	if !ok || openAPIVersion.Kind != yaml.ScalarNode || !strings.HasPrefix(openAPIVersion.Value, "3.1.") {
		return nil, fmt.Errorf("generated document must be OpenAPI 3.1")
	}

	setMappingValue(root, "info", mappingNode(
		"title", scalarNode("Scribe Connect API"),
		"version", scalarNode("v1"),
		"description", scalarNode("Workspace-scoped APIs for handwritten-document ingest, transcription, IIIF annotation editing, and publication."),
	))

	components, ok := mappingValue(root, "components")
	if !ok || components.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("generated document has no components mapping")
	}
	setMappingValue(components, "securitySchemes", securitySchemesNode())
	setMappingValue(root, "security", securityRequirementsNode(SessionCookieScheme, APIKeyScheme, BearerScheme))

	paths, ok := mappingValue(root, "paths")
	if !ok || paths.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("generated document has no paths mapping")
	}
	seen := make(map[string]struct{}, len(procedures))
	for _, procedure := range procedures {
		if !strings.HasPrefix(procedure.Path, "/scribe.v1.") {
			return nil, fmt.Errorf("invalid Scribe procedure path %q", procedure.Path)
		}
		if _, exists := seen[procedure.Path]; exists {
			return nil, fmt.Errorf("duplicate procedure %s", procedure.Path)
		}
		seen[procedure.Path] = struct{}{}
		pathItem, ok := mappingValue(paths, procedure.Path)
		if !ok || pathItem.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("generated OpenAPI is missing path %s", procedure.Path)
		}
		operation, ok := mappingValue(pathItem, "post")
		if !ok || operation.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("generated OpenAPI path %s has no POST operation", procedure.Path)
		}
		removeMappingValue(operation, "security")
		switch {
		case procedure.AllowAnonymous:
			setMappingValue(operation, "security", sequenceNode())
		case procedure.SessionOnly:
			setMappingValue(operation, "security", securityRequirementsNode(SessionCookieScheme))
		}
	}

	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, fmt.Errorf("encode OpenAPI: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close OpenAPI encoder: %w", err)
	}
	return output.Bytes(), nil
}

func securitySchemesNode() *yaml.Node {
	return mappingNode(
		SessionCookieScheme, mappingNode(
			"type", scalarNode("apiKey"),
			"in", scalarNode("cookie"),
			"name", scalarNode("scribe_session"),
			"description", scalarNode("Interactive browser session cookie. The cookie name is deployment-configurable; scribe_session is the default."),
		),
		APIKeyScheme, mappingNode(
			"type", scalarNode("apiKey"),
			"in", scalarNode("header"),
			"name", scalarNode("X-Scribe-API-Key"),
			"description", scalarNode("Workspace-scoped Scribe API key."),
		),
		BearerScheme, mappingNode(
			"type", scalarNode("http"),
			"scheme", scalarNode("bearer"),
			"bearerFormat", scalarNode("Scribe API key or registered external JWT"),
		),
	)
}

func securityRequirementsNode(schemes ...string) *yaml.Node {
	requirements := sequenceNode()
	for _, scheme := range schemes {
		requirements.Content = append(requirements.Content, mappingNode(scheme, sequenceNode()))
	}
	return requirements
}

func documentMapping(document *yaml.Node) (*yaml.Node, error) {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("generated OpenAPI root must be a mapping")
	}
	return document.Content[0], nil
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, false
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1], true
		}
	}
	return nil, false
}

func setMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, scalarNode(key), value)
}

func removeMappingValue(mapping *yaml.Node, key string) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
			return
		}
	}
}

func mappingNode(values ...any) *yaml.Node {
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for index := 0; index < len(values); index += 2 {
		mapping.Content = append(mapping.Content, scalarNode(values[index].(string)), values[index+1].(*yaml.Node))
	}
	return mapping
}

func sequenceNode(values ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: values}
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
