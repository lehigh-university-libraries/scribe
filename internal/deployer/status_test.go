package deployer

import (
	"strings"
	"testing"
)

func TestResolveStatusPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     Mode
		preview  bool
		outcomes map[Step]Outcome
		want     string
	}{
		{name: "production plan failure", mode: ModeApply, outcomes: outcomes(StepPlan, OutcomeFailure), want: "failure"},
		{name: "production apply failure", mode: ModeApply, outcomes: outcomes(StepPlan, OutcomeSuccess, StepApply, OutcomeFailure), want: "failure"},
		{name: "production apply cancellation", mode: ModeApply, outcomes: outcomes(StepPlan, OutcomeSuccess, StepApply, OutcomeCancelled), want: "cancelled"},
		{name: "preview apply failure", mode: ModeApply, preview: true, outcomes: outcomes(StepPlanPreview, OutcomeSuccess, StepApplyPreview, OutcomeFailure), want: "failure"},
		{name: "attestation failure", mode: ModeApply, outcomes: outcomes(StepPlan, OutcomeSuccess, StepApply, OutcomeSuccess, StepRevision, OutcomeFailure), want: "attestation-failure"},
		{name: "readiness failure", mode: ModeApply, outcomes: outcomes(StepPlan, OutcomeSuccess, StepApply, OutcomeSuccess, StepRevision, OutcomeSuccess, StepReadiness, OutcomeFailure), want: "failure"},
		{name: "URL failure", mode: ModeApply, outcomes: successfulApplyOutcomes(StepURL, OutcomeFailure), want: "url-failure"},
		{name: "preview URL failure", mode: ModeApply, preview: true, outcomes: outcomes(StepPlanPreview, OutcomeSuccess, StepApplyPreview, OutcomeSuccess, StepRevision, OutcomeSuccess, StepReadiness, OutcomeSuccess, StepURL, OutcomeFailure), want: "url-failure"},
		{name: "backup failure", mode: ModeApply, outcomes: successfulApplyOutcomes(StepBackup, OutcomeFailure), want: "backup-verification-failure"},
		{name: "production success", mode: ModeApply, outcomes: successfulApplyOutcomes(), want: "success"},
		{name: "preview success", mode: ModeApply, preview: true, outcomes: outcomes(StepPlanPreview, OutcomeSuccess, StepApplyPreview, OutcomeSuccess, StepRevision, OutcomeSuccess, StepReadiness, OutcomeSuccess, StepURL, OutcomeSuccess), want: "success"},
		{name: "production plan status", mode: ModePlan, outcomes: outcomes(StepPlan, OutcomeFailure), want: "failure"},
		{name: "preview plan status", mode: ModePlan, preview: true, outcomes: outcomes(StepPlanPreview, OutcomeSuccess), want: "success"},
		{name: "production destroy status", mode: ModeDestroy, outcomes: outcomes(StepDestroy, OutcomeFailure), want: "failure"},
		{name: "preview destroy failure", mode: ModeDestroy, preview: true, outcomes: outcomes(StepDestroyPreview, OutcomeFailure), want: "failure"},
		{name: "preview Vault cleanup skipped", mode: ModeDestroy, preview: true, outcomes: outcomes(StepDestroyPreview, OutcomeSuccess), want: "vault-cleanup-skipped"},
		{name: "preview Vault cleanup failure", mode: ModeDestroy, preview: true, outcomes: outcomes(StepDestroyPreview, OutcomeSuccess, StepDestroyPreviewVault, OutcomeFailure), want: "vault-cleanup-failure"},
		{name: "preview destroy success", mode: ModeDestroy, preview: true, outcomes: outcomes(StepDestroyPreview, OutcomeSuccess, StepDestroyPreviewVault, OutcomeSuccess), want: "success"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveStatus(StatusInput{Mode: test.mode, Preview: test.preview, Outcomes: test.outcomes})
			if err != nil {
				t.Fatalf("ResolveStatus returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolveStatus = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveStatusRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	if _, err := ResolveStatus(StatusInput{Mode: "release"}); err == nil || !strings.Contains(err.Error(), "DEPLOY_MODE") {
		t.Fatalf("invalid mode error = %v", err)
	}
	if _, err := ResolveStatus(StatusInput{
		Mode:     ModePlan,
		Outcomes: outcomes(StepPlan, Outcome("neutral")),
	}); err == nil || !strings.Contains(err.Error(), "PLAN_OUTCOME") {
		t.Fatalf("invalid outcome error = %v", err)
	}
}

func outcomes(values ...any) map[Step]Outcome {
	result := make(map[Step]Outcome, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		result[values[index].(Step)] = values[index+1].(Outcome)
	}
	return result
}

func successfulApplyOutcomes(overrides ...any) map[Step]Outcome {
	result := outcomes(
		StepPlan, OutcomeSuccess,
		StepApply, OutcomeSuccess,
		StepRevision, OutcomeSuccess,
		StepReadiness, OutcomeSuccess,
		StepURL, OutcomeSuccess,
		StepBackup, OutcomeSuccess,
	)
	for step, value := range outcomes(overrides...) {
		result[step] = value
	}
	return result
}
