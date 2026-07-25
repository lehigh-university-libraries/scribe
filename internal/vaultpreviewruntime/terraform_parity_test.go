package vaultpreviewruntime

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestTerraformPreviewPolicyAndRoleMatchReconciler(t *testing.T) {
	t.Parallel()

	source := readTerraformVaultSource(t)
	assertTerraformLocal(t, source, "vault_preview_policy_name", strconv.Quote(policyName))
	assertTerraformLocal(t, source, "vault_gcp_auth_backend_path", strconv.Quote(strings.TrimSuffix(gcpAuthPath, "/")))

	const accessorExpression = "${vault_gcp_auth_backend.gcp[0].accessor}"
	policyBody := terraformPreviewPolicyBody(t, source)
	if count := strings.Count(policyBody, accessorExpression); count != 1 {
		t.Fatalf("preview policy accessor expression count = %d, want 1", count)
	}
	renderedPolicy := strings.Replace(policyBody, accessorExpression, testAccessor, 1)
	if want := renderPolicy(testAccessor); renderedPolicy != want {
		t.Fatalf("Terraform preview policy rendered as %q, want %q", renderedPolicy, want)
	}

	roleBlock := terraformSimpleResourceBlock(t, source, "vault_gcp_auth_backend_role", "preview_app")
	assertOnlyTerraformAttributes(t, roleBlock, []string{
		"count",
		"backend",
		"role",
		"type",
		"bound_service_accounts",
		"bound_projects",
		"allow_gce_inference",
		"token_no_default_policy",
		"token_ttl",
		"token_max_ttl",
		"token_policies",
		"depends_on",
	})

	write := canonicalRoleWrite(testProject)
	read := exactRole(testProject)
	assertCanonicalRoleReadParity(t, write, read)

	assertTerraformAttribute(t, roleBlock, "backend", "vault_gcp_auth_backend.gcp[0].path")
	assertTerraformAttribute(t, roleBlock, "role", "local.vault_preview_policy_name")
	assertTerraformAttribute(t, roleBlock, "type", strconv.Quote(write.Type))
	assertTerraformAttribute(t, roleBlock, "bound_service_accounts", terraformStringList(write.BoundServiceAccounts))
	if len(write.BoundProjects) != 1 || write.BoundProjects[0] != testProject {
		t.Fatalf("canonical bound projects = %v, want [%s]", write.BoundProjects, testProject)
	}
	assertTerraformAttribute(t, roleBlock, "bound_projects", "[var.project_id]")
	assertTerraformAttribute(t, roleBlock, "allow_gce_inference", strconv.FormatBool(write.AllowGCEInference))
	assertTerraformAttribute(t, roleBlock, "token_no_default_policy", strconv.FormatBool(write.TokenNoDefaultPolicy))
	assertTerraformAttribute(t, roleBlock, "token_ttl", strconv.FormatInt(write.TokenTTL, 10))
	assertTerraformAttribute(t, roleBlock, "token_max_ttl", strconv.FormatInt(write.TokenMaxTTL, 10))
	if len(write.TokenPolicies) != 1 || write.TokenPolicies[0] != policyName {
		t.Fatalf("canonical token policies = %v, want [%s]", write.TokenPolicies, policyName)
	}
	assertTerraformAttribute(t, roleBlock, "token_policies", "[local.vault_preview_policy_name,]")
}

func assertCanonicalRoleReadParity(t *testing.T, write roleWrite, read roleRead) {
	t.Helper()
	checks := map[string]bool{
		"type":                    write.Type == read.Type,
		"bound_service_accounts":  sameStrings(write.BoundServiceAccounts, read.BoundServiceAccounts),
		"bound_projects":          sameStrings(write.BoundProjects, read.BoundProjects),
		"add_group_aliases":       write.AddGroupAliases == read.AddGroupAliases,
		"alias_metadata":          len(write.AliasMetadata) == 0 && len(read.AliasMetadata) == 0,
		"max_jwt_exp":             write.MaxJWTExp == read.MaxJWTExp,
		"allow_gce_inference":     write.AllowGCEInference == read.AllowGCEInference,
		"token_bound_cidrs":       sameStrings(write.TokenBoundCIDRs, read.TokenBoundCIDRs),
		"token_explicit_max_ttl":  write.TokenExplicitMaxTTL == read.TokenExplicitMaxTTL,
		"token_no_default_policy": write.TokenNoDefaultPolicy == read.TokenNoDefaultPolicy,
		"token_num_uses":          write.TokenNumUses == read.TokenNumUses,
		"token_period":            write.TokenPeriod == read.TokenPeriod,
		"token_ttl":               write.TokenTTL == read.TokenTTL,
		"token_max_ttl":           write.TokenMaxTTL == read.TokenMaxTTL,
		"token_policies":          sameStrings(write.TokenPolicies, read.TokenPolicies),
		"token_type":              write.TokenType == read.TokenType,
	}
	for field, exact := range checks {
		if !exact {
			t.Errorf("canonical write and exact read disagree on %s", field)
		}
	}
}

func readTerraformVaultSource(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve parity test source path")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "terraform", "vault.tf")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read terraform/vault.tf: %v", err)
	}
	return string(raw)
}

func terraformPreviewPolicyBody(t *testing.T, source string) string {
	t.Helper()
	pattern := regexp.MustCompile(
		`(?ms)^resource\s+"vault_policy"\s+"preview_app"\s+\{.*?^[ \t]*policy\s*=\s*<<-EOT[ \t]*\r?\n(.*?)^EOT[ \t]*\r?$`,
	)
	matches := pattern.FindAllStringSubmatch(source, -1)
	if len(matches) != 1 {
		t.Fatalf("Terraform preview policy body matches = %d, want 1", len(matches))
	}
	return matches[0][1]
}

func terraformSimpleResourceBlock(t *testing.T, source, resourceType, name string) string {
	t.Helper()
	header := regexp.MustCompile(
		`(?m)^resource\s+` + regexp.QuoteMeta(strconv.Quote(resourceType)) +
			`\s+` + regexp.QuoteMeta(strconv.Quote(name)) + `\s+\{\s*$`,
	)
	locations := header.FindAllStringIndex(source, -1)
	if len(locations) != 1 {
		t.Fatalf("Terraform resource %s.%s matches = %d, want 1", resourceType, name, len(locations))
	}
	remainder := source[locations[0][1]:]
	end := strings.Index(remainder, "\n}\n")
	if end < 0 {
		t.Fatalf("Terraform resource %s.%s has no closing brace", resourceType, name)
	}
	return remainder[:end]
}

func assertTerraformLocal(t *testing.T, source, name, want string) {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(name) + `[ \t]*=[ \t]*([^\r\n#]+)`)
	matches := pattern.FindAllStringSubmatch(source, -1)
	if len(matches) != 1 {
		t.Fatalf("Terraform local %s matches = %d, want 1", name, len(matches))
	}
	if got := compactTerraformValue(matches[0][1]); got != compactTerraformValue(want) {
		t.Fatalf("Terraform local %s = %q, want %q", name, got, compactTerraformValue(want))
	}
}

func assertTerraformAttribute(t *testing.T, block, name, want string) {
	t.Helper()
	got, count := terraformAttribute(block, name)
	if count != 1 {
		t.Fatalf("Terraform preview role attribute %s matches = %d, want 1", name, count)
	}
	if got = compactTerraformValue(got); got != compactTerraformValue(want) {
		t.Fatalf("Terraform preview role attribute %s = %q, want %q", name, got, compactTerraformValue(want))
	}
}

func terraformAttribute(block, name string) (string, int) {
	lines := strings.Split(block, "\n")
	var value string
	count := 0
	for lineNumber := 0; lineNumber < len(lines); lineNumber++ {
		line := strings.TrimSpace(lines[lineNumber])
		left, right, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(left) != name {
			continue
		}
		count++
		current := strings.TrimSpace(right)
		value = current
		depth := strings.Count(current, "[") - strings.Count(current, "]")
		for depth > 0 && lineNumber+1 < len(lines) {
			lineNumber++
			current = strings.TrimSpace(lines[lineNumber])
			value += current
			depth += strings.Count(current, "[") - strings.Count(current, "]")
		}
	}
	return value, count
}

func assertOnlyTerraformAttributes(t *testing.T, block string, allowed []string) {
	t.Helper()
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	identifier := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	for _, line := range strings.Split(block, "\n") {
		left, _, found := strings.Cut(strings.TrimSpace(line), "=")
		name := strings.TrimSpace(left)
		if !found || !identifier.MatchString(name) {
			continue
		}
		if _, ok := allowedSet[name]; !ok {
			t.Errorf("Terraform preview role has unverified attribute %q", name)
		}
	}
}

func compactTerraformValue(value string) string {
	return strings.Map(func(character rune) rune {
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' {
			return -1
		}
		return character
	}, value)
}

func terraformStringList(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = strconv.Quote(value)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}
