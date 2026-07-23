package providersecret

import "testing"

func TestValidateVaultPathIsStrictlyWorkspaceScoped(t *testing.T) {
	t.Parallel()
	if err := ValidateVaultPath("scribe/dev/provider-secrets/workspaces", 7, "scribe/dev/provider-secrets/workspaces/7/openai/key-1"); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
	for _, path := range []string{
		"scribe/dev/provider-secrets/workspaces/8/openai/key-1",
		"scribe/dev/provider-secrets/workspaces/70/openai/key-1",
		"scribe/dev/provider-secrets/workspaces/7",
		"scribe/dev/provider-secrets/workspaces/7/../8/key-1",
		"scribe/dev/provider-secrets/workspaces/7//key-1",
		" scribe/dev/provider-secrets/workspaces/7/openai/key-1",
		"scribe/dev/provider-secrets/workspaces/7/openai/key-1 ",
	} {
		if err := ValidateVaultPath("scribe/dev/provider-secrets/workspaces", 7, path); err == nil {
			t.Errorf("unsafe path %q was accepted", path)
		}
	}
}
