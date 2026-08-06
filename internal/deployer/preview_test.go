package deployer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	testRepository         = "example/scribe"
	testProject            = "example-project"
	testPreviewMachineType = "n2d-standard-2"
	testRegion             = "us-east5"
	testZone               = "us-east5-c"
	testMainSHA            = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHeadSHA            = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestResolvePreviewEventInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		action          string
		baseRef         string
		previousBaseRef string
		headRepository  string
		wantMode        PreviewMode
		wantNotice      string
	}{
		{name: "main pull request", action: "synchronize", baseRef: "main", headRepository: testRepository, wantMode: PreviewModeApply},
		{name: "closed main pull request", action: "closed", baseRef: "main", headRepository: testRepository, wantMode: PreviewModeDestroy},
		{name: "retargeted pull request", action: "edited", baseRef: "feature", previousBaseRef: "main", headRepository: testRepository, wantMode: PreviewModeDestroy, wantNotice: "retargeted away from main"},
		{name: "unrelated pull request", action: "opened", baseRef: "feature", headRepository: testRepository, wantMode: PreviewModeSkip, wantNotice: "does not target main"},
		{name: "fork pull request", action: "opened", baseRef: "main", headRepository: "fork/scribe", wantMode: PreviewModeSkip, wantNotice: "Fork pull request"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			github := &fakeGitHub{mainSHA: testMainSHA}
			inputs, err := ResolvePreviewInputs(context.Background(), PreviewRequest{
				Repository:           testRepository,
				ProjectID:            testProject,
				PreviewMachineType:   testPreviewMachineType,
				Region:               testRegion,
				Zone:                 testZone,
				EventAction:          test.action,
				EventBaseRef:         test.baseRef,
				EventPreviousBaseRef: test.previousBaseRef,
				EventHeadRepository:  test.headRepository,
				EventHeadSHA:         testHeadSHA,
				EventPR:              "75",
			}, github)
			if err != nil {
				t.Fatalf("ResolvePreviewInputs returned error: %v", err)
			}
			if inputs.Mode != test.wantMode {
				t.Fatalf("mode = %q, want %q", inputs.Mode, test.wantMode)
			}
			if !strings.Contains(inputs.Notice, test.wantNotice) {
				t.Fatalf("notice = %q, want substring %q", inputs.Notice, test.wantNotice)
			}
			assertPreviewInputs(t, inputs)
			if github.pullCalls != 0 {
				t.Fatalf("event resolver made %d pull-request API calls", github.pullCalls)
			}
		})
	}
}

func TestResolvePreviewDispatchInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		action      string
		wantMode    PreviewMode
		wantRecover bool
	}{
		{action: "deploy", wantMode: PreviewModeApply},
		{action: "destroy", wantMode: PreviewModeDestroy},
		{action: "recover-destroy", wantMode: PreviewModeDestroy, wantRecover: true},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			t.Parallel()
			github := &fakeGitHub{
				mainSHA: testMainSHA,
				pullRequest: PullRequest{
					BaseRef:        "main",
					HeadRepository: testRepository,
					HeadSHA:        testHeadSHA,
				},
			}
			inputs, err := ResolvePreviewInputs(context.Background(), dispatchRequest(test.action), github)
			if err != nil {
				t.Fatalf("ResolvePreviewInputs returned error: %v", err)
			}
			if inputs.Mode != test.wantMode || inputs.RecoverDestroy != test.wantRecover {
				t.Fatalf("mode/recovery = %q/%t, want %q/%t", inputs.Mode, inputs.RecoverDestroy, test.wantMode, test.wantRecover)
			}
			assertPreviewInputs(t, inputs)
			if github.pullCalls != 1 {
				t.Fatalf("dispatch resolver made %d pull-request API calls, want 1", github.pullCalls)
			}
		})
	}
}

func TestResolvePreviewReviewedMachineTypes(t *testing.T) {
	t.Parallel()

	for _, machineType := range []string{"e2-medium", "n2d-standard-2"} {
		t.Run(machineType, func(t *testing.T) {
			t.Parallel()
			request := dispatchRequest("deploy")
			request.PreviewMachineType = machineType
			inputs, err := ResolvePreviewInputs(context.Background(), request, validDispatchGitHub())
			if err != nil {
				t.Fatalf("ResolvePreviewInputs returned error: %v", err)
			}
			if inputs.PreviewMachineType != machineType {
				t.Fatalf("preview machine type = %q, want %q", inputs.PreviewMachineType, machineType)
			}
		})
	}
}

func TestResolvePreviewInputsFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request PreviewRequest
		github  *fakeGitHub
		want    string
	}{
		{name: "unprotected dispatch ref", request: withDispatchOverride(func(request *PreviewRequest) { request.WorkflowRef = "refs/heads/feature" }), github: validDispatchGitHub(), want: "protected main"},
		{name: "invalid dispatch action", request: dispatchRequest("repair"), github: validDispatchGitHub(), want: "action must be deploy"},
		{name: "fork dispatch", request: dispatchRequest("deploy"), github: &fakeGitHub{mainSHA: testMainSHA, pullRequest: PullRequest{BaseRef: "main", HeadRepository: "fork/scribe", HeadSHA: testHeadSHA}}, want: "fork pull requests"},
		{name: "non-main deploy", request: dispatchRequest("deploy"), github: &fakeGitHub{mainSHA: testMainSHA, pullRequest: PullRequest{BaseRef: "feature", HeadRepository: testRepository, HeadSHA: testHeadSHA}}, want: "targeting main"},
		{name: "invalid protected SHA", request: dispatchRequest("deploy"), github: &fakeGitHub{mainSHA: "main", pullRequest: validDispatchGitHub().pullRequest}, want: "invalid protected-main SHA"},
		{name: "invalid head SHA", request: dispatchRequest("deploy"), github: &fakeGitHub{mainSHA: testMainSHA, pullRequest: PullRequest{BaseRef: "main", HeadRepository: testRepository, HeadSHA: "head"}}, want: "invalid PR head SHA"},
		{name: "project output injection", request: withDispatchOverride(func(request *PreviewRequest) { request.ProjectID = testProject + "\nmode=apply" }), github: validDispatchGitHub(), want: "valid Google Cloud project"},
		{name: "missing preview machine type", request: withDispatchOverride(func(request *PreviewRequest) { request.PreviewMachineType = "" }), github: validDispatchGitHub(), want: "explicitly reviewed preview machine type"},
		{name: "preview machine type injection", request: withDispatchOverride(func(request *PreviewRequest) { request.PreviewMachineType = "n2d-standard-2\nmode=apply" }), github: validDispatchGitHub(), want: "explicitly reviewed preview machine type"},
		{name: "unreviewed preview machine type", request: withDispatchOverride(func(request *PreviewRequest) { request.PreviewMachineType = "n2d-standard-96" }), github: validDispatchGitHub(), want: "explicitly reviewed preview machine type"},
		{name: "zone outside region", request: withDispatchOverride(func(request *PreviewRequest) { request.Zone = "us-west1-b" }), github: validDispatchGitHub(), want: "belong to SCRIBE_REGION"},
		{name: "main lookup failure", request: dispatchRequest("deploy"), github: &fakeGitHub{mainErr: errors.New("token-canary")}, want: "protected-main SHA: failed"},
		{name: "pull lookup failure", request: dispatchRequest("deploy"), github: &fakeGitHub{mainSHA: testMainSHA, pullErr: errors.New("token-canary")}, want: "pull request: failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolvePreviewInputs(context.Background(), test.request, test.github)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if strings.Contains(err.Error(), "token-canary") {
				t.Fatalf("error exposed dependency details: %v", err)
			}
		})
	}
}

func TestPreviewInputsWriteGitHubOutput(t *testing.T) {
	t.Parallel()

	inputs, err := ResolvePreviewInputs(context.Background(), dispatchRequest("recover-destroy"), validDispatchGitHub())
	if err != nil {
		t.Fatalf("ResolvePreviewInputs returned error: %v", err)
	}
	var output bytes.Buffer
	if err := inputs.WriteGitHubOutput(&output); err != nil {
		t.Fatalf("WriteGitHubOutput returned error: %v", err)
	}
	for _, line := range []string{
		"pr_number=75",
		"head_sha=" + testHeadSHA,
		"base_sha=" + testMainSHA,
		"mode=destroy",
		"recover_destroy_inputs=true",
		"preview_machine_type=" + testPreviewMachineType,
		"zone=us-east5-c",
		"frontend_gar_image_tag=us-docker.pkg.dev/example-project/internal/scribe-frontend:pr-75",
		"backend_origin=http://scribe-pr-75.us-east5-c.c.example-project.internal",
	} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Errorf("output does not contain %q: %s", line, output.String())
		}
	}
	if strings.Contains(output.String(), "frontend_gar_image=") {
		t.Fatalf("output contains unused untagged GAR image: %s", output.String())
	}
}

func TestPreviewInputsWriteGitHubOutputRejectsInjection(t *testing.T) {
	t.Parallel()

	inputs, err := ResolvePreviewInputs(context.Background(), dispatchRequest("deploy"), validDispatchGitHub())
	if err != nil {
		t.Fatalf("ResolvePreviewInputs returned error: %v", err)
	}
	inputs.BackendOrigin += "\nuntrusted=true"
	var output bytes.Buffer
	if err := inputs.WriteGitHubOutput(&output); err == nil {
		t.Fatal("WriteGitHubOutput accepted a newline")
	}
	if output.Len() != 0 {
		t.Fatalf("WriteGitHubOutput emitted partial output: %q", output.String())
	}
}

func TestPreviewInputsWriteGitHubOutputRejectsUnreviewedMachineType(t *testing.T) {
	t.Parallel()

	inputs, err := ResolvePreviewInputs(context.Background(), dispatchRequest("deploy"), validDispatchGitHub())
	if err != nil {
		t.Fatalf("ResolvePreviewInputs returned error: %v", err)
	}
	inputs.PreviewMachineType = "n2d-standard-96"
	var output bytes.Buffer
	if err := inputs.WriteGitHubOutput(&output); err == nil {
		t.Fatal("WriteGitHubOutput accepted an unreviewed preview machine type")
	}
	if output.Len() != 0 {
		t.Fatalf("WriteGitHubOutput emitted partial output: %q", output.String())
	}
}

type fakeGitHub struct {
	mainSHA     string
	mainErr     error
	pullRequest PullRequest
	pullErr     error
	pullCalls   int
}

func (github *fakeGitHub) MainSHA(context.Context, string) (string, error) {
	return github.mainSHA, github.mainErr
}

func (github *fakeGitHub) PullRequest(_ context.Context, _, _ string) (PullRequest, error) {
	github.pullCalls++
	return github.pullRequest, github.pullErr
}

func dispatchRequest(action string) PreviewRequest {
	return PreviewRequest{
		Repository:         testRepository,
		ProjectID:          testProject,
		PreviewMachineType: testPreviewMachineType,
		Region:             testRegion,
		Zone:               testZone,
		WorkflowRef:        "refs/heads/main",
		DispatchAction:     action,
		DispatchPR:         "75",
	}
}

func withDispatchOverride(update func(*PreviewRequest)) PreviewRequest {
	request := dispatchRequest("deploy")
	update(&request)
	return request
}

func validDispatchGitHub() *fakeGitHub {
	return &fakeGitHub{
		mainSHA: testMainSHA,
		pullRequest: PullRequest{
			BaseRef:        "main",
			HeadRepository: testRepository,
			HeadSHA:        testHeadSHA,
		},
	}
}

func assertPreviewInputs(t *testing.T, inputs PreviewInputs) {
	t.Helper()
	if inputs.PRNumber != "75" || inputs.HeadSHA != testHeadSHA || inputs.BaseSHA != testMainSHA {
		t.Fatalf("unexpected immutable identity: %+v", inputs)
	}
	if inputs.Region != testRegion || inputs.Zone != testZone || inputs.Tag != "pr-75" {
		t.Fatalf("unexpected location/tag: %+v", inputs)
	}
	if inputs.PreviewMachineType != testPreviewMachineType {
		t.Fatalf("preview machine type = %q, want %q", inputs.PreviewMachineType, testPreviewMachineType)
	}
}
