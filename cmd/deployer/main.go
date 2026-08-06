// Command deployer provides typed, unit-tested deployment decisions to thin
// GitHub Actions shell entrypoints.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/lehigh-university-libraries/scribe/internal/deployer"
)

type dependencies struct {
	getenv func(string) string
	github deployer.GitHub
	open   func(string) (io.WriteCloser, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, dependencies{
		getenv: os.Getenv,
		github: ghClient{},
		open:   openGitHubOutput,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "deployer: %v\n", err)
		os.Exit(1)
	}
}

func openGitHubOutput(path string) (io.WriteCloser, error) {
	clean := filepath.Clean(path)
	if path == "" || !filepath.IsAbs(clean) || clean != path {
		return nil, errors.New("GITHUB_OUTPUT path is invalid")
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return nil, errors.New("inspect GITHUB_OUTPUT: failed")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("GITHUB_OUTPUT must be a regular file")
	}
	// #nosec G304 -- GitHub creates this validated absolute regular file and
	// exposes its path through GITHUB_OUTPUT; it is not repository input.
	return os.OpenFile(clean, os.O_WRONLY|os.O_APPEND, 0)
}

func run(ctx context.Context, args []string, stdout io.Writer, deps dependencies) error {
	if len(args) != 1 {
		return errors.New("usage: deployer status|preview-inputs|runtime-overrides")
	}
	if deps.getenv == nil {
		return errors.New("environment reader is not configured")
	}

	switch args[0] {
	case "status":
		status, err := deployer.ResolveStatus(deployer.StatusInput{
			Mode:    deployer.Mode(deps.getenv("DEPLOY_MODE")),
			Preview: deps.getenv("PR_NUMBER") != "",
			Outcomes: map[deployer.Step]deployer.Outcome{
				deployer.StepPlan:                deployer.Outcome(deps.getenv(string(deployer.StepPlan))),
				deployer.StepPlanPreview:         deployer.Outcome(deps.getenv(string(deployer.StepPlanPreview))),
				deployer.StepApply:               deployer.Outcome(deps.getenv(string(deployer.StepApply))),
				deployer.StepApplyPreview:        deployer.Outcome(deps.getenv(string(deployer.StepApplyPreview))),
				deployer.StepRevision:            deployer.Outcome(deps.getenv(string(deployer.StepRevision))),
				deployer.StepURL:                 deployer.Outcome(deps.getenv(string(deployer.StepURL))),
				deployer.StepReadiness:           deployer.Outcome(deps.getenv(string(deployer.StepReadiness))),
				deployer.StepRollback:            deployer.Outcome(deps.getenv(string(deployer.StepRollback))),
				deployer.StepBackup:              deployer.Outcome(deps.getenv(string(deployer.StepBackup))),
				deployer.StepDestroy:             deployer.Outcome(deps.getenv(string(deployer.StepDestroy))),
				deployer.StepDestroyPreview:      deployer.Outcome(deps.getenv(string(deployer.StepDestroyPreview))),
				deployer.StepDestroyPreviewVault: deployer.Outcome(deps.getenv(string(deployer.StepDestroyPreviewVault))),
			},
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(stdout, status); err != nil {
			return errors.New("write deployment status: failed")
		}
		return nil

	case "preview-inputs":
		if deps.github == nil || deps.open == nil {
			return errors.New("preview resolver dependencies are not configured")
		}
		outputPath := deps.getenv("GITHUB_OUTPUT")
		if outputPath == "" {
			return errors.New("GITHUB_OUTPUT is required")
		}
		inputs, err := deployer.ResolvePreviewInputs(ctx, deployer.PreviewRequest{
			Repository:           deps.getenv("GITHUB_REPOSITORY"),
			ProjectID:            deps.getenv("GCLOUD_PROJECT"),
			PreviewMachineType:   deps.getenv("SCRIBE_PREVIEW_MACHINE_TYPE"),
			Region:               deps.getenv("SCRIBE_REGION"),
			Zone:                 deps.getenv("SCRIBE_ZONE"),
			WorkflowRef:          deps.getenv("WORKFLOW_REF"),
			DispatchAction:       deps.getenv("DISPATCH_ACTION"),
			DispatchPR:           deps.getenv("DISPATCH_PR"),
			EventAction:          deps.getenv("EVENT_ACTION"),
			EventBaseRef:         deps.getenv("EVENT_BASE_REF"),
			EventPreviousBaseRef: deps.getenv("EVENT_PREVIOUS_BASE_REF"),
			EventHeadRepository:  deps.getenv("EVENT_HEAD_REPO"),
			EventHeadSHA:         deps.getenv("EVENT_HEAD_SHA"),
			EventPR:              deps.getenv("EVENT_PR"),
		}, deps.github)
		if err != nil {
			return err
		}
		if inputs.Notice != "" {
			if _, err := fmt.Fprintln(stdout, inputs.Notice); err != nil {
				return errors.New("write preview notice: failed")
			}
		}
		output, err := deps.open(outputPath)
		if err != nil {
			return errors.New("open GITHUB_OUTPUT: failed")
		}
		if err := inputs.WriteGitHubOutput(output); err != nil {
			_ = output.Close()
			return err
		}
		if err := output.Close(); err != nil {
			return errors.New("close GITHUB_OUTPUT: failed")
		}
		return nil

	case "runtime-overrides":
		return deployer.WriteRuntimeOverrides(stdout, deps.getenv)

	default:
		return errors.New("usage: deployer status|preview-inputs|runtime-overrides")
	}
}

type ghClient struct{}

func (ghClient) MainSHA(ctx context.Context, repository string) (string, error) {
	var response struct {
		SHA string `json:"sha"`
	}
	if err := runGHJSON(ctx, &response,
		"api", "repos/"+repository+"/commits/main", "--jq", `{sha: .sha}`,
	); err != nil {
		return "", err
	}
	return response.SHA, nil
}

func (ghClient) PullRequest(ctx context.Context, repository, number string) (deployer.PullRequest, error) {
	var response struct {
		BaseRef        string `json:"base_ref"`
		HeadRepository string `json:"head_repository"`
		HeadSHA        string `json:"head_sha"`
	}
	if err := runGHJSON(ctx, &response,
		"api", "repos/"+repository+"/pulls/"+number, "--jq",
		`{base_ref: .base.ref, head_repository: .head.repo.full_name, head_sha: .head.sha}`,
	); err != nil {
		return deployer.PullRequest{}, err
	}
	return deployer.PullRequest{
		BaseRef:        response.BaseRef,
		HeadRepository: response.HeadRepository,
		HeadSHA:        response.HeadSHA,
	}, nil
}

func runGHJSON(ctx context.Context, destination any, args ...string) error {
	// #nosec G204 -- gh is invoked directly without a shell; every dynamic
	// repository, pull-request, and endpoint component is validated first by
	// ResolvePreviewInputs and passed as one argv element.
	output, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		return errors.New("GitHub API request failed")
	}
	if err := json.Unmarshal(output, destination); err != nil {
		return errors.New("GitHub API returned malformed metadata")
	}
	return nil
}
