package auth

import "testing"

func TestValidateProviderSecretVaultPathRequiresWorkspacePrefix(t *testing.T) {
	manager := &Manager{providerSecretsVaultPrefix: "scribe/dev/provider-secrets/workspaces"}

	if err := manager.validateProviderSecretVaultPath("scribe/dev/provider-secrets/workspaces/7/gemini/key", 7); err != nil {
		t.Fatalf("validateProviderSecretVaultPath returned error for scoped path: %v", err)
	}

	for _, path := range []string{
		"scribe/dev/provider-secrets/workspaces/8/gemini/key",
		"scribe/dev/provider-secrets/workspaces/7/../8/gemini/key",
		"scribe/dev/provider-secrets/workspaces/7//gemini/key",
		"scribe/prod/provider-secrets/workspaces/7/gemini/key",
	} {
		t.Run(path, func(t *testing.T) {
			if err := manager.validateProviderSecretVaultPath(path, 7); err == nil {
				t.Fatalf("validateProviderSecretVaultPath accepted unsafe path %q", path)
			}
		})
	}
}
