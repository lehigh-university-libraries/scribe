package productionbrowserreadiness

import (
	"context"
	"errors"
)

// These statuses are emitted only by the attested VM-side mint operation.
// Controller-side gcloud failures use their generic category unless a mint
// invocation returns one of these exact values.
const (
	remoteMintSessionCommandExitCode = 91 + iota
	remoteMintStateExportExitCode
	remoteMintContainerCleanupExitCode
	remoteMintHostStateContractExitCode
	remoteMintRecoveryCleanupExitCode
)

type remoteMintStage uint8

const (
	remoteMintSucceeded remoteMintStage = iota
	remoteMintSessionCommand
	remoteMintStateExport
	remoteMintContainerCleanup
	remoteMintHostStateContract
	remoteMintRecoveryCleanup
)

var (
	errRemoteMintSessionCommand    = errors.New("production browser remote mint session command failed")
	errRemoteMintStateExport       = errors.New("production browser remote mint state export failed")
	errRemoteMintContainerCleanup  = errors.New("production browser remote mint container cleanup failed")
	errRemoteMintHostStateContract = errors.New("production browser remote mint host state contract failed")
	errRemoteMintRecoveryCleanup   = errors.New("production browser remote mint recovery cleanup failed")
)

func remoteMintExitCode(ctx context.Context, stage remoteMintStage) int {
	if stage == remoteMintSucceeded {
		return ExitSuccess
	}
	if contextStopped(ctx) {
		return contextExitCode(ctx)
	}
	switch stage {
	case remoteMintSessionCommand:
		return remoteMintSessionCommandExitCode
	case remoteMintStateExport:
		return remoteMintStateExportExitCode
	case remoteMintContainerCleanup:
		return remoteMintContainerCleanupExitCode
	case remoteMintHostStateContract:
		return remoteMintHostStateContractExitCode
	case remoteMintRecoveryCleanup:
		return remoteMintRecoveryCleanupExitCode
	default:
		return ExitInvalidInvocation
	}
}

func remoteInvocationError(mode remoteMode, exitCode int) error {
	if mode != remoteMint {
		return errCommandFailed
	}
	switch exitCode {
	case remoteMintSessionCommandExitCode:
		return errRemoteMintSessionCommand
	case remoteMintStateExportExitCode:
		return errRemoteMintStateExport
	case remoteMintContainerCleanupExitCode:
		return errRemoteMintContainerCleanup
	case remoteMintHostStateContractExitCode:
		return errRemoteMintHostStateContract
	case remoteMintRecoveryCleanupExitCode:
		return errRemoteMintRecoveryCleanup
	default:
		return errCommandFailed
	}
}

func remoteMintCategory(ctx context.Context, err error) string {
	if contextStopped(ctx) {
		return "interrupted"
	}
	switch {
	case errors.Is(err, errRemoteMintSessionCommand):
		return "remote-mint-session-command"
	case errors.Is(err, errRemoteMintStateExport):
		return "remote-mint-state-export"
	case errors.Is(err, errRemoteMintContainerCleanup):
		return "remote-mint-container-cleanup"
	case errors.Is(err, errRemoteMintHostStateContract):
		return "remote-mint-host-state-contract"
	case errors.Is(err, errRemoteMintRecoveryCleanup):
		return "remote-mint-recovery-cleanup"
	default:
		return "remote-mint"
	}
}
