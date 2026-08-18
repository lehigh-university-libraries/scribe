// Package deployer owns deterministic deployment orchestration decisions that
// are shared by GitHub Actions and local contract tests.
package deployer

import "fmt"

// Mode is a supported Terraform deployment operation.
type Mode string

const (
	ModeApply   Mode = "apply"
	ModePlan    Mode = "plan"
	ModeDestroy Mode = "destroy"
)

// Outcome is a GitHub Actions step outcome.
type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomeFailure   Outcome = "failure"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeSkipped   Outcome = "skipped"
)

// Step identifies a workflow step whose outcome contributes to the published
// deployment status.
type Step string

const (
	StepPlan                Step = "PLAN_OUTCOME"
	StepPlanPreview         Step = "PLAN_PREVIEW_OUTCOME"
	StepApply               Step = "APPLY_OUTCOME"
	StepApplyPreview        Step = "APPLY_PREVIEW_OUTCOME"
	StepRevision            Step = "REVISION_OUTCOME"
	StepURL                 Step = "URL_OUTCOME"
	StepReadiness           Step = "READINESS_OUTCOME"
	StepBackup              Step = "BACKUP_OUTCOME"
	StepDestroy             Step = "DESTROY_OUTCOME"
	StepDestroyPreview      Step = "DESTROY_PREVIEW_OUTCOME"
	StepDestroyPreviewVault Step = "DESTROY_PREVIEW_VAULT_OUTCOME"
)

// StatusInput contains the workflow state needed to publish one stable result.
// Missing outcomes have the same meaning as GitHub's skipped outcome.
type StatusInput struct {
	Mode     Mode
	Preview  bool
	Outcomes map[Step]Outcome
}

// ResolveStatus applies the deployment result precedence contract.
func ResolveStatus(input StatusInput) (string, error) {
	outcome := func(step Step) (Outcome, error) {
		value := input.Outcomes[step]
		if value == "" {
			value = OutcomeSkipped
		}
		switch value {
		case OutcomeSuccess, OutcomeFailure, OutcomeCancelled, OutcomeSkipped:
			return value, nil
		default:
			return "", fmt.Errorf("invalid step outcome for %s", step)
		}
	}

	switch input.Mode {
	case ModeApply:
		planStep := StepPlan
		applyStep := StepApply
		if input.Preview {
			planStep = StepPlanPreview
			applyStep = StepApplyPreview
		}

		plan, err := outcome(planStep)
		if err != nil {
			return "", err
		}
		if plan != OutcomeSuccess {
			return string(plan), nil
		}

		apply, err := outcome(applyStep)
		if err != nil {
			return "", err
		}
		if apply != OutcomeSuccess {
			return string(apply), nil
		}

		revision, err := outcome(StepRevision)
		if err != nil {
			return "", err
		}
		if revision != OutcomeSuccess {
			return "attestation-" + string(revision), nil
		}

		readiness, err := outcome(StepReadiness)
		if err != nil {
			return "", err
		}
		if readiness != OutcomeSuccess {
			return string(readiness), nil
		}

		url, err := outcome(StepURL)
		if err != nil {
			return "", err
		}
		if url != OutcomeSuccess {
			return "url-" + string(url), nil
		}
		if !input.Preview {
			backup, backupErr := outcome(StepBackup)
			if backupErr != nil {
				return "", backupErr
			}
			if backup != OutcomeSuccess {
				return "backup-verification-" + string(backup), nil
			}
		}
		return "success", nil

	case ModePlan:
		step := StepPlan
		if input.Preview {
			step = StepPlanPreview
		}
		result, err := outcome(step)
		return string(result), err

	case ModeDestroy:
		if !input.Preview {
			result, err := outcome(StepDestroy)
			return string(result), err
		}
		destroy, err := outcome(StepDestroyPreview)
		if err != nil {
			return "", err
		}
		if destroy != OutcomeSuccess {
			return string(destroy), nil
		}
		vault, err := outcome(StepDestroyPreviewVault)
		if err != nil {
			return "", err
		}
		if vault != OutcomeSuccess {
			return "vault-cleanup-" + string(vault), nil
		}
		return "success", nil

	default:
		return "", fmt.Errorf("DEPLOY_MODE must be apply, plan, or destroy")
	}
}
