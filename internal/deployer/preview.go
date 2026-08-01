package deployer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	projectPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	regionPattern     = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	prNumberPattern   = regexp.MustCompile(`^[1-9][0-9]*$`)
	zonePattern       = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+-[a-z]$`)
)

// PreviewMode is the action selected for one pull-request preview.
type PreviewMode string

const (
	PreviewModeApply   PreviewMode = "apply"
	PreviewModeDestroy PreviewMode = "destroy"
	PreviewModeSkip    PreviewMode = "skip"
)

// PullRequest is the immutable subset of GitHub pull-request data used by the
// protected preview resolver.
type PullRequest struct {
	BaseRef        string
	HeadRepository string
	HeadSHA        string
}

// GitHub resolves protected-main and pull-request metadata without exposing a
// token or response body to orchestration errors.
type GitHub interface {
	MainSHA(context.Context, string) (string, error)
	PullRequest(context.Context, string, string) (PullRequest, error)
}

// PreviewRequest contains trusted workflow context. EventPR selects the
// pull_request_target path; an empty EventPR selects the protected manual
// dispatch path.
type PreviewRequest struct {
	Repository string
	ProjectID  string
	Region     string
	Zone       string

	WorkflowRef    string
	DispatchAction string
	DispatchPR     string

	EventAction          string
	EventBaseRef         string
	EventPreviousBaseRef string
	EventHeadRepository  string
	EventHeadSHA         string
	EventPR              string
}

// PreviewInputs are safe newline-free values written to GITHUB_OUTPUT.
type PreviewInputs struct {
	PRNumber            string
	HeadSHA             string
	BaseSHA             string
	Mode                PreviewMode
	RecoverDestroy      bool
	Region              string
	Tag                 string
	Zone                string
	ImageTag            string
	FrontendImageTag    string
	FrontendGARImageTag string
	BackendOrigin       string
	Notice              string
}

// ResolvePreviewInputs selects apply, destroy, or skip using protected main as
// the privileged source. It never derives the infrastructure checkout from PR
// base data, including when a PR has been retargeted.
func ResolvePreviewInputs(ctx context.Context, request PreviewRequest, github GitHub) (PreviewInputs, error) {
	if github == nil {
		return PreviewInputs{}, errors.New("GitHub resolver is required")
	}
	if !repositoryPattern.MatchString(request.Repository) {
		return PreviewInputs{}, errors.New("GITHUB_REPOSITORY is invalid")
	}
	if !projectPattern.MatchString(request.ProjectID) {
		return PreviewInputs{}, errors.New("GCLOUD_PROJECT must be a valid Google Cloud project ID")
	}
	if !regionPattern.MatchString(request.Region) {
		return PreviewInputs{}, errors.New("SCRIBE_REGION must be a valid Google Cloud region")
	}
	if !zonePattern.MatchString(request.Zone) || !strings.HasPrefix(request.Zone, request.Region+"-") {
		return PreviewInputs{}, errors.New("SCRIBE_ZONE must belong to SCRIBE_REGION")
	}

	mainSHA, err := github.MainSHA(ctx, request.Repository)
	if err != nil {
		return PreviewInputs{}, errors.New("resolve protected-main SHA: failed")
	}
	if !commitPattern.MatchString(mainSHA) {
		return PreviewInputs{}, errors.New("GitHub returned an invalid protected-main SHA")
	}

	var (
		prNumber       string
		headSHA        string
		mode           PreviewMode
		recoverDestroy bool
		notice         string
	)
	if request.EventPR != "" {
		prNumber = request.EventPR
		headSHA = request.EventHeadSHA
		switch {
		case request.EventHeadRepository != request.Repository:
			mode = PreviewModeSkip
			notice = "Fork pull request detected; skipping credentialed preview deployment."
		case request.EventAction == "closed" && (request.EventBaseRef == "main" || request.EventPreviousBaseRef == "main"):
			mode = PreviewModeDestroy
		case request.EventBaseRef == "main":
			mode = PreviewModeApply
		case request.EventPreviousBaseRef == "main":
			mode = PreviewModeDestroy
			notice = "Pull request was retargeted away from main; destroying its preview."
		default:
			mode = PreviewModeSkip
			notice = "Pull request does not target main; no preview is required."
		}
	} else {
		if request.WorkflowRef != "refs/heads/main" {
			return PreviewInputs{}, errors.New("preview dispatches must run from the protected main branch")
		}
		if !prNumberPattern.MatchString(request.DispatchPR) {
			return PreviewInputs{}, errors.New("pr_number must be numeric")
		}
		prNumber = request.DispatchPR
		pullRequest, pullErr := github.PullRequest(ctx, request.Repository, prNumber)
		if pullErr != nil {
			return PreviewInputs{}, errors.New("resolve pull request: failed")
		}
		if pullRequest.HeadRepository != request.Repository {
			return PreviewInputs{}, errors.New("fork pull requests cannot be deployed with repository credentials")
		}
		switch request.DispatchAction {
		case "deploy":
			if pullRequest.BaseRef != "main" {
				return PreviewInputs{}, errors.New("only pull requests targeting main can create previews")
			}
			mode = PreviewModeApply
		case "destroy":
			mode = PreviewModeDestroy
		case "recover-destroy":
			mode = PreviewModeDestroy
			recoverDestroy = true
		default:
			return PreviewInputs{}, errors.New("action must be deploy, destroy, or recover-destroy")
		}
		headSHA = pullRequest.HeadSHA
	}

	if !commitPattern.MatchString(headSHA) {
		return PreviewInputs{}, errors.New("invalid PR head SHA")
	}
	if !prNumberPattern.MatchString(prNumber) {
		return PreviewInputs{}, errors.New("invalid PR number")
	}

	tag := "pr-" + prNumber
	garRepository := "us-docker.pkg.dev/" + request.ProjectID + "/internal"
	return PreviewInputs{
		PRNumber:            prNumber,
		HeadSHA:             headSHA,
		BaseSHA:             mainSHA,
		Mode:                mode,
		RecoverDestroy:      recoverDestroy,
		Region:              request.Region,
		Tag:                 tag,
		Zone:                request.Zone,
		ImageTag:            "ghcr.io/lehigh-university-libraries/scribe:" + tag,
		FrontendImageTag:    "ghcr.io/lehigh-university-libraries/scribe-frontend:" + tag,
		FrontendGARImageTag: garRepository + "/scribe-frontend:" + tag,
		BackendOrigin:       fmt.Sprintf("http://scribe-pr-%s.%s.c.%s.internal", prNumber, request.Zone, request.ProjectID),
		Notice:              notice,
	}, nil
}

// WriteGitHubOutput emits the exact output names consumed by the preview
// workflow. Every value is validated before this method is reachable.
func (inputs PreviewInputs) WriteGitHubOutput(writer io.Writer) error {
	for _, value := range []string{
		inputs.PRNumber,
		inputs.HeadSHA,
		inputs.BaseSHA,
		string(inputs.Mode),
		inputs.Region,
		inputs.Tag,
		inputs.Zone,
		inputs.ImageTag,
		inputs.FrontendImageTag,
		inputs.FrontendGARImageTag,
		inputs.BackendOrigin,
	} {
		if value == "" || strings.ContainsAny(value, "\r\n") {
			return errors.New("GitHub output value is invalid")
		}
	}
	if inputs.Mode != PreviewModeApply && inputs.Mode != PreviewModeDestroy && inputs.Mode != PreviewModeSkip {
		return errors.New("GitHub output mode is invalid")
	}
	_, err := fmt.Fprintf(writer,
		"pr_number=%s\nhead_sha=%s\nbase_sha=%s\nmode=%s\nrecover_destroy_inputs=%t\nregion=%s\ntag=%s\nzone=%s\nimage_tag=%s\nfrontend_image_tag=%s\nfrontend_gar_image_tag=%s\nbackend_origin=%s\n",
		inputs.PRNumber,
		inputs.HeadSHA,
		inputs.BaseSHA,
		inputs.Mode,
		inputs.RecoverDestroy,
		inputs.Region,
		inputs.Tag,
		inputs.Zone,
		inputs.ImageTag,
		inputs.FrontendImageTag,
		inputs.FrontendGARImageTag,
		inputs.BackendOrigin,
	)
	if err != nil {
		return errors.New("write GitHub outputs: failed")
	}
	return nil
}
