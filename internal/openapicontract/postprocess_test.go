package openapicontract

import (
	"strings"
	"testing"

	_ "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/reflect/protoregistry"
	"gopkg.in/yaml.v3"
)

func TestRewritePublishesAuthenticationAndRequiresEveryOperation(t *testing.T) {
	t.Parallel()
	source := []byte(`openapi: 3.1.0
info: {}
paths:
  /scribe.v1.TestService/Public:
    post:
      operationId: public
  /scribe.v1.TestService/Session:
    post:
      operationId: session
  /scribe.v1.TestService/Authenticated:
    post:
      operationId: authenticated
components:
  schemas:
    Existing: {type: object}
security: []
`)
	output, err := Rewrite(source, []Procedure{
		{Path: "/scribe.v1.TestService/Public", AllowAnonymous: true},
		{Path: "/scribe.v1.TestService/Session", SessionOnly: true},
		{Path: "/scribe.v1.TestService/Authenticated"},
	})
	if err != nil {
		t.Fatalf("Rewrite() error = %v", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(output, &document); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	info := document["info"].(map[string]any)
	if info["title"] != "Scribe Connect API" || info["version"] != "v1" {
		t.Fatalf("info = %#v", info)
	}
	components := document["components"].(map[string]any)
	schemes := components["securitySchemes"].(map[string]any)
	for _, name := range []string{SessionCookieScheme, APIKeyScheme, BearerScheme} {
		if _, ok := schemes[name]; !ok {
			t.Errorf("security scheme %q is missing", name)
		}
	}
	paths := document["paths"].(map[string]any)
	public := paths["/scribe.v1.TestService/Public"].(map[string]any)["post"].(map[string]any)
	if security, ok := public["security"].([]any); !ok || len(security) != 0 {
		t.Errorf("anonymous operation security = %#v", public["security"])
	}
	session := paths["/scribe.v1.TestService/Session"].(map[string]any)["post"].(map[string]any)
	if got := session["security"].([]any)[0].(map[string]any); len(got) != 1 || got[SessionCookieScheme] == nil {
		t.Errorf("session operation security = %#v", session["security"])
	}
	authenticated := paths["/scribe.v1.TestService/Authenticated"].(map[string]any)["post"].(map[string]any)
	if _, ok := authenticated["security"]; ok {
		t.Errorf("ordinary authenticated operation should inherit global security: %#v", authenticated)
	}
	if _, ok := components["schemas"].(map[string]any)["Existing"]; !ok {
		t.Error("existing generated components were discarded")
	}

	_, err = Rewrite(source, []Procedure{{Path: "/scribe.v1.TestService/Missing"}})
	if err == nil || !strings.Contains(err.Error(), "missing path") {
		t.Fatalf("missing procedure error = %v", err)
	}
	_, err = Rewrite([]byte(strings.Replace(string(source), "    post:\n", "", 1)), []Procedure{{Path: "/scribe.v1.TestService/Public"}})
	if err == nil || !strings.Contains(err.Error(), "no POST operation") {
		t.Fatalf("empty streaming-style path error = %v", err)
	}
}

func TestScribeProceduresFollowAuthorizationDescriptors(t *testing.T) {
	t.Parallel()
	procedures, err := ScribeProcedures(protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("ScribeProcedures() error = %v", err)
	}
	byPath := make(map[string]Procedure, len(procedures))
	for _, procedure := range procedures {
		byPath[procedure.Path] = procedure
	}
	if got := byPath["/scribe.v1.AuthService/GetAuthMe"]; !got.AllowAnonymous || got.SessionOnly {
		t.Errorf("GetAuthMe procedure = %+v", got)
	}
	if got := byPath["/scribe.v1.AuthService/CreateAPIKey"]; got.AllowAnonymous || !got.SessionOnly {
		t.Errorf("CreateAPIKey procedure = %+v", got)
	}
	if got := byPath["/scribe.v1.TranscriptionService/StreamTranscriptionJob"]; got.AllowAnonymous || got.SessionOnly {
		t.Errorf("StreamTranscriptionJob procedure = %+v", got)
	}
}
