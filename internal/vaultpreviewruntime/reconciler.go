// Package vaultpreviewruntime reconciles the shared Vault policy and GCP auth
// role used by Scribe preview runtimes.
package vaultpreviewruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/servicehttp"
	"golang.org/x/oauth2"
)

const (
	// SuccessMarker is the only success output emitted by the command.
	SuccessMarker = "[preview-vault-runtime] policy=true role=true"

	policyName             = "scribe-preview-app"
	gcpAuthPath            = "gcp/"
	maxResponseBytes int64 = 128 << 10
	maxSecretBytes         = 16 << 10
	requestTimeout         = 30 * time.Second
)

var (
	projectIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	regionPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[0-9]$`)
	projectNumberPattern = regexp.MustCompile(`^[1-9][0-9]{5,19}$`)
	accessorPattern      = regexp.MustCompile(`^auth_gcp_[A-Za-z0-9]+$`)
	roleIDPattern        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Mode controls whether reconciliation may change Vault.
type Mode string

const (
	// ModeCheck verifies the exact policy and role without changing Vault.
	ModeCheck Mode = "check"
	// ModeApply idempotently converges the exact policy and role.
	ModeApply Mode = "apply"
)

// Config contains the trusted runtime inputs and injectable dependencies.
type Config struct {
	VaultAddress     string
	VaultToken       string
	ProjectID        string
	ProjectNumber    string
	Region           string
	AdminTokenSource oauth2.TokenSource
	HTTPClient       *http.Client
}

// Reconciler verifies and converges the shared preview runtime configuration.
type Reconciler struct {
	baseURL          string
	vaultToken       string
	projectID        string
	adminTokenSource oauth2.TokenSource
	httpClient       *http.Client
}

// New constructs a fail-closed preview runtime reconciler.
func New(config Config) (*Reconciler, error) {
	vaultToken := config.VaultToken
	if !validSecretHeaderValue(vaultToken) {
		return nil, errors.New("VAULT_TOKEN is missing or invalid")
	}
	projectID := config.ProjectID
	if !projectIDPattern.MatchString(projectID) {
		return nil, errors.New("GCLOUD_PROJECT is invalid")
	}
	projectNumber := config.ProjectNumber
	if !projectNumberPattern.MatchString(projectNumber) {
		return nil, errors.New("GCLOUD_PROJECT_NUMBER is invalid")
	}
	region := config.Region
	if !regionPattern.MatchString(region) {
		return nil, errors.New("SCRIBE_REGION is invalid")
	}
	baseURL, err := validateVaultAddress(config.VaultAddress, projectNumber, region)
	if err != nil {
		return nil, err
	}
	if config.AdminTokenSource == nil {
		return nil, errors.New("google admin token source is required")
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = servicehttp.NewClient(requestTimeout)
	} else {
		cloned := *httpClient
		httpClient = &cloned
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return servicehttp.ErrRedirectBlocked
		}
		if httpClient.Timeout <= 0 || httpClient.Timeout > requestTimeout {
			httpClient.Timeout = requestTimeout
		}
	}

	return &Reconciler{
		baseURL:          baseURL,
		vaultToken:       vaultToken,
		projectID:        projectID,
		adminTokenSource: config.AdminTokenSource,
		httpClient:       httpClient,
	}, nil
}

// Reconcile checks or applies the exact preview policy and role.
func (r *Reconciler) Reconcile(ctx context.Context, mode Mode) error {
	if r == nil {
		return errors.New("preview Vault runtime reconciler is not configured")
	}
	if ctx == nil {
		return errors.New("preview Vault runtime context is required")
	}
	if mode != ModeCheck && mode != ModeApply {
		return errors.New("preview Vault runtime mode is invalid")
	}
	adminToken, err := r.adminToken()
	if err != nil {
		return err
	}

	accessor, err := r.gcpAccessor(ctx, adminToken)
	if err != nil {
		return err
	}
	if err := r.verifyGCPConfig(ctx, adminToken); err != nil {
		return err
	}
	desiredPolicy := renderPolicy(accessor)
	desiredRole := exactRole(r.projectID)

	policy, policyFound, err := r.readPolicy(ctx, adminToken)
	if err != nil {
		return err
	}
	role, roleFound, err := r.readRole(ctx, adminToken)
	if err != nil {
		return err
	}
	policyExact := policyFound && policy == desiredPolicy
	roleExact := roleFound && role.exact(desiredRole)

	if mode == ModeCheck {
		if !policyExact || !roleExact {
			return errors.New("preview Vault runtime configuration is not exact")
		}
		return nil
	}

	changed := false
	if !policyExact {
		if err := r.writeJSON(ctx, adminToken, http.MethodPut, "/v1/sys/policies/acl/"+policyName, policyWrite{
			Policy: desiredPolicy,
		}); err != nil {
			return err
		}
		changed = true
	}
	if !roleExact {
		// Vault's GCP auth plugin cannot change a role's type in place. Only
		// that immutable drift requires a brief delete/recreate gap. Every
		// other field is converged atomically with a complete canonical write.
		if roleFound && role.Type != desiredRole.Type {
			if err := r.writeJSON(ctx, adminToken, http.MethodDelete, "/v1/auth/gcp/role/"+policyName, nil); err != nil {
				return err
			}
		}
		if err := r.writeJSON(ctx, adminToken, http.MethodPut, "/v1/auth/gcp/role/"+policyName, canonicalRoleWrite(r.projectID)); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}

	policy, policyFound, err = r.readPolicy(ctx, adminToken)
	if err != nil {
		return err
	}
	role, roleFound, err = r.readRole(ctx, adminToken)
	if err != nil {
		return err
	}
	if !policyFound || policy != desiredPolicy || !roleFound || !role.exact(desiredRole) {
		return errors.New("preview Vault runtime readback is not exact")
	}
	return nil
}

func (r *Reconciler) adminToken() (string, error) {
	token, err := r.adminTokenSource.Token()
	if err != nil || token == nil || !token.Valid() || !validSecretHeaderValue(token.AccessToken) {
		return "", errors.New("acquire Google admin token: failed")
	}
	return token.AccessToken, nil
}

func (r *Reconciler) gcpAccessor(ctx context.Context, adminToken string) (string, error) {
	body, found, err := r.read(ctx, adminToken, "/v1/sys/auth")
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("vault GCP auth backend is missing")
	}
	data, err := responseData(body)
	if err != nil {
		return "", errors.New("decode Vault auth backends: invalid response")
	}

	var mounts map[string]json.RawMessage
	if err := json.Unmarshal(data, &mounts); err != nil || mounts == nil {
		return "", errors.New("decode Vault auth backends: invalid response")
	}
	var exact authMount
	exactFound := false
	for path, raw := range mounts {
		var mount authMount
		if err := json.Unmarshal(raw, &mount); err != nil {
			return "", errors.New("decode Vault auth backends: invalid response")
		}
		if path == gcpAuthPath {
			exact = mount
			exactFound = true
		}
	}
	if !exactFound {
		return "", errors.New("vault GCP auth backend is missing")
	}
	if exact.Type != "gcp" {
		return "", errors.New("vault GCP auth backend has the wrong type")
	}
	if !accessorPattern.MatchString(exact.Accessor) {
		return "", errors.New("vault GCP auth backend accessor is invalid")
	}
	return exact.Accessor, nil
}

func (r *Reconciler) verifyGCPConfig(ctx context.Context, adminToken string) error {
	body, found, err := r.read(ctx, adminToken, "/v1/auth/gcp/config")
	if err != nil {
		return err
	}
	if !found {
		return errors.New("vault GCP auth backend config is missing")
	}
	data, err := mountedResponseData(body, "gcp")
	if err != nil {
		return errors.New("decode Vault GCP auth backend config: invalid response")
	}
	var config gcpConfigRead
	if err := json.Unmarshal(data, &config); err != nil ||
		config.IAMAlias != "unique_id" ||
		!slices.Equal(config.IAMMetadata, []string{"service_account_email"}) ||
		len(config.CustomEndpoint) != 0 ||
		config.ClientEmail != "" ||
		config.ClientID != "" ||
		config.PrivateKeyID != "" ||
		config.CredentialsProjectID != "" ||
		config.ServiceAccountEmail != "" ||
		config.IdentityTokenAudience != "" ||
		config.IdentityTokenTTL != 0 {
		return errors.New("vault GCP auth backend config is not exact")
	}
	return nil
}

func (r *Reconciler) readPolicy(ctx context.Context, adminToken string) (string, bool, error) {
	body, found, err := r.read(ctx, adminToken, "/v1/sys/policies/acl/"+policyName)
	if err != nil || !found {
		return "", found, err
	}
	data, err := responseData(body)
	if err != nil {
		return "", false, errors.New("decode Vault preview policy: invalid response")
	}
	var policy policyRead
	if err := decodeExact(data, &policy); err != nil || policy.Name != policyName {
		return "", false, errors.New("decode Vault preview policy: invalid response")
	}
	return policy.Policy, true, nil
}

func (r *Reconciler) readRole(ctx context.Context, adminToken string) (roleRead, bool, error) {
	body, found, err := r.read(ctx, adminToken, "/v1/auth/gcp/role/"+policyName)
	if err != nil || !found {
		return roleRead{}, found, err
	}
	data, err := mountedResponseData(body, "gcp")
	if err != nil {
		return roleRead{}, false, errors.New("decode Vault preview role: invalid response")
	}
	var discriminator roleTypeRead
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return roleRead{}, false, errors.New("decode Vault preview role: invalid response")
	}
	switch discriminator.Type {
	case "iam":
		var role roleRead
		if err := decodeExact(data, &role); err != nil || !roleIDPattern.MatchString(role.RoleID) {
			return roleRead{}, false, errors.New("decode Vault preview role: invalid response")
		}
		return role, true, nil
	case "gce":
		var role gceRoleRead
		if err := decodeExact(data, &role); err != nil ||
			role.Type != "gce" ||
			!roleIDPattern.MatchString(role.RoleID) {
			return roleRead{}, false, errors.New("decode Vault preview role: invalid response")
		}
		// GCE-only fields are accepted solely to identify the exact named role
		// as an immutable wrong type. Apply immediately replaces it with the
		// canonical IAM role; check mode remains read-only and fails.
		return roleRead{RoleID: role.RoleID, Type: role.Type}, true, nil
	default:
		return roleRead{}, false, errors.New("decode Vault preview role: invalid response")
	}
}

func (r *Reconciler) read(ctx context.Context, adminToken, path string) ([]byte, bool, error) {
	body, status, err := r.request(ctx, adminToken, http.MethodGet, path, nil)
	if err != nil {
		return nil, false, err
	}
	if status == http.StatusNotFound {
		return nil, false, nil
	}
	if status < 200 || status >= 300 {
		return nil, false, fmt.Errorf("vault read returned HTTP %d", status)
	}
	return body, true, nil
}

func (r *Reconciler) writeJSON(ctx context.Context, adminToken, method, path string, value any) error {
	var payload []byte
	var err error
	if value != nil {
		payload, err = json.Marshal(value)
		if err != nil {
			return errors.New("encode Vault reconciliation request: failed")
		}
	}
	body, status, err := r.request(ctx, adminToken, method, path, payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("vault write returned HTTP %d", status)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if _, err := responseEnvelope(body); err != nil {
		return errors.New("decode Vault write response: invalid response")
	}
	return nil
}

func (r *Reconciler) request(ctx context.Context, adminToken, method, path string, payload []byte) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, body)
	if err != nil {
		return nil, 0, errors.New("construct Vault request: failed")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Vault-Token", r.vaultToken)
	req.Header.Set("X-Admin-Token", adminToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, 0, errors.New("send Vault request: failed")
	}
	defer resp.Body.Close()

	if resp.ContentLength > maxResponseBytes {
		return nil, 0, errors.New("read Vault response: response too large")
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, 0, errors.New("read Vault response: failed")
	}
	if int64(len(responseBody)) > maxResponseBytes {
		return nil, 0, errors.New("read Vault response: response too large")
	}
	return responseBody, resp.StatusCode, nil
}

func validateVaultAddress(raw, projectNumber, region string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.Host != parsed.Hostname() ||
		parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawPath != "" ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery ||
		parsed.Fragment != "" {
		return "", errors.New("VAULT_ADDR must be the exact trusted preview Vault HTTPS origin")
	}
	expectedHost := "vault-server-dev-" + projectNumber + "." + region + ".run.app"
	if parsed.Hostname() != expectedHost {
		return "", errors.New("VAULT_ADDR must be the exact trusted preview Vault HTTPS origin")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validSecretHeaderValue(value string) bool {
	if value == "" || len(value) > maxSecretBytes {
		return false
	}
	for index := range len(value) {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func renderPolicy(accessor string) string {
	return fmt.Sprintf(
		"path \"secret/data/scribe/previews/{{identity.entity.aliases.%s.metadata.service_account_email}}/database/app\" {\n  capabilities = [\"read\"]\n}\n",
		accessor,
	)
}

func exactRole(projectID string) roleRead {
	return roleRead{
		Type:                 "iam",
		BoundServiceAccounts: []string{"*"},
		BoundProjects:        []string{projectID},
		AddGroupAliases:      false,
		MaxJWTExp:            900,
		AllowGCEInference:    false,
		TokenBoundCIDRs:      []string{},
		TokenExplicitMaxTTL:  0,
		TokenMaxTTL:          900,
		TokenNoDefaultPolicy: true,
		TokenPeriod:          0,
		TokenPolicies:        []string{policyName},
		TokenType:            "default",
		TokenTTL:             300,
		TokenNumUses:         0,
		AliasMetadata:        map[string]string{},
		DeprecatedPolicies:   []string{},
		DeprecatedTTL:        0,
		DeprecatedMaxTTL:     0,
		DeprecatedPeriod:     0,
	}
}

func canonicalRoleWrite(projectID string) roleWrite {
	return roleWrite{
		Type:                 "iam",
		BoundServiceAccounts: []string{"*"},
		BoundProjects:        []string{projectID},
		AddGroupAliases:      false,
		AliasMetadata:        map[string]string{},
		MaxJWTExp:            900,
		AllowGCEInference:    false,
		TokenBoundCIDRs:      []string{},
		TokenExplicitMaxTTL:  0,
		TokenMaxTTL:          900,
		TokenNoDefaultPolicy: true,
		TokenNumUses:         0,
		TokenPeriod:          0,
		TokenPolicies:        []string{policyName},
		TokenTTL:             300,
		TokenType:            "default",
	}
}

func responseData(body []byte) (json.RawMessage, error) {
	envelope, err := responseEnvelope(body)
	if err != nil || len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
		return nil, errors.New("response data is missing")
	}
	return envelope.Data, nil
}

func mountedResponseData(body []byte, mountType string) (json.RawMessage, error) {
	envelope, err := responseEnvelope(body)
	if err != nil ||
		envelope.MountType != mountType ||
		len(envelope.Data) == 0 ||
		bytes.Equal(envelope.Data, []byte("null")) {
		return nil, errors.New("mounted response data is invalid")
	}
	return envelope.Data, nil
}

func responseEnvelope(body []byte) (vaultEnvelope, error) {
	if err := rejectDuplicateJSONFields(body); err != nil {
		return vaultEnvelope{}, err
	}
	var envelope vaultEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return vaultEnvelope{}, err
	}
	if len(envelope.Errors) != 0 {
		return vaultEnvelope{}, errors.New("vault response reported errors")
	}
	return envelope, nil
}

func decodeExact(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON data")
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		fields := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, exists := fields[key]; exists {
				return errors.New("duplicate JSON object key")
			}
			fields[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

type vaultEnvelope struct {
	Data      json.RawMessage `json:"data"`
	Errors    []string        `json:"errors"`
	MountType string          `json:"mount_type"`
}

type authMount struct {
	Type     string `json:"type"`
	Accessor string `json:"accessor"`
}

type policyRead struct {
	Name   string `json:"name"`
	Policy string `json:"policy"`
}

type policyWrite struct {
	Policy string `json:"policy"`
}

type gcpConfigRead struct {
	IAMAlias              string            `json:"iam_alias"`
	IAMMetadata           []string          `json:"iam_metadata"`
	CustomEndpoint        map[string]string `json:"custom_endpoint"`
	ClientEmail           string            `json:"client_email"`
	ClientID              string            `json:"client_id"`
	PrivateKeyID          string            `json:"private_key_id"`
	CredentialsProjectID  string            `json:"project_id"`
	ServiceAccountEmail   string            `json:"service_account_email"`
	IdentityTokenAudience string            `json:"identity_token_audience"`
	IdentityTokenTTL      int64             `json:"identity_token_ttl"`
}

type roleTypeRead struct {
	Type string `json:"type"`
}

type roleWrite struct {
	Type                 string            `json:"type"`
	BoundServiceAccounts []string          `json:"bound_service_accounts"`
	BoundProjects        []string          `json:"bound_projects"`
	AddGroupAliases      bool              `json:"add_group_aliases"`
	AliasMetadata        map[string]string `json:"alias_metadata"`
	MaxJWTExp            int64             `json:"max_jwt_exp"`
	AllowGCEInference    bool              `json:"allow_gce_inference"`
	TokenBoundCIDRs      []string          `json:"token_bound_cidrs"`
	TokenExplicitMaxTTL  int64             `json:"token_explicit_max_ttl"`
	TokenMaxTTL          int64             `json:"token_max_ttl"`
	TokenNoDefaultPolicy bool              `json:"token_no_default_policy"`
	TokenNumUses         int               `json:"token_num_uses"`
	TokenPeriod          int64             `json:"token_period"`
	TokenPolicies        []string          `json:"token_policies"`
	TokenTTL             int64             `json:"token_ttl"`
	TokenType            string            `json:"token_type"`
}

type roleRead struct {
	RoleID               string            `json:"role_id"`
	Type                 string            `json:"type"`
	BoundServiceAccounts []string          `json:"bound_service_accounts"`
	BoundProjects        []string          `json:"bound_projects"`
	AddGroupAliases      bool              `json:"add_group_aliases"`
	MaxJWTExp            int64             `json:"max_jwt_exp"`
	AllowGCEInference    bool              `json:"allow_gce_inference"`
	TokenBoundCIDRs      []string          `json:"token_bound_cidrs"`
	TokenExplicitMaxTTL  int64             `json:"token_explicit_max_ttl"`
	TokenMaxTTL          int64             `json:"token_max_ttl"`
	TokenNoDefaultPolicy bool              `json:"token_no_default_policy"`
	TokenPeriod          int64             `json:"token_period"`
	TokenPolicies        []string          `json:"token_policies"`
	TokenType            string            `json:"token_type"`
	TokenTTL             int64             `json:"token_ttl"`
	TokenNumUses         int               `json:"token_num_uses"`
	AliasMetadata        map[string]string `json:"alias_metadata,omitempty"`
	DeprecatedPolicies   []string          `json:"policies,omitempty"`
	DeprecatedTTL        int64             `json:"ttl,omitempty"`
	DeprecatedMaxTTL     int64             `json:"max_ttl,omitempty"`
	DeprecatedPeriod     int64             `json:"period,omitempty"`
}

// gceRoleRead mirrors the response fields emitted by the repository-pinned
// vault-plugin-auth-gcp v0.22.0. It is never treated as desired state.
type gceRoleRead struct {
	RoleID               string            `json:"role_id"`
	Type                 string            `json:"type"`
	BoundServiceAccounts []string          `json:"bound_service_accounts"`
	BoundProjects        []string          `json:"bound_projects"`
	AddGroupAliases      bool              `json:"add_group_aliases"`
	AliasMetadata        map[string]string `json:"alias_metadata,omitempty"`
	BoundRegions         []string          `json:"bound_regions,omitempty"`
	BoundZones           []string          `json:"bound_zones,omitempty"`
	BoundInstanceGroups  []string          `json:"bound_instance_groups,omitempty"`
	BoundLabels          map[string]string `json:"bound_labels,omitempty"`
	TokenBoundCIDRs      []string          `json:"token_bound_cidrs"`
	TokenExplicitMaxTTL  int64             `json:"token_explicit_max_ttl"`
	TokenMaxTTL          int64             `json:"token_max_ttl"`
	TokenNoDefaultPolicy bool              `json:"token_no_default_policy"`
	TokenPeriod          int64             `json:"token_period"`
	TokenPolicies        []string          `json:"token_policies"`
	TokenType            string            `json:"token_type"`
	TokenTTL             int64             `json:"token_ttl"`
	TokenNumUses         int               `json:"token_num_uses"`
	DeprecatedPolicies   []string          `json:"policies,omitempty"`
	DeprecatedTTL        int64             `json:"ttl,omitempty"`
	DeprecatedMaxTTL     int64             `json:"max_ttl,omitempty"`
	DeprecatedPeriod     int64             `json:"period,omitempty"`
}

func (r roleRead) exact(want roleRead) bool {
	return r.Type == want.Type &&
		sameStrings(r.BoundServiceAccounts, want.BoundServiceAccounts) &&
		sameStrings(r.BoundProjects, want.BoundProjects) &&
		r.AddGroupAliases == want.AddGroupAliases &&
		r.MaxJWTExp == want.MaxJWTExp &&
		r.AllowGCEInference == want.AllowGCEInference &&
		sameStrings(r.TokenBoundCIDRs, want.TokenBoundCIDRs) &&
		r.TokenExplicitMaxTTL == want.TokenExplicitMaxTTL &&
		r.TokenMaxTTL == want.TokenMaxTTL &&
		r.TokenNoDefaultPolicy == want.TokenNoDefaultPolicy &&
		r.TokenPeriod == want.TokenPeriod &&
		sameStrings(r.TokenPolicies, want.TokenPolicies) &&
		r.TokenType == want.TokenType &&
		r.TokenTTL == want.TokenTTL &&
		r.TokenNumUses == want.TokenNumUses &&
		len(r.AliasMetadata) == 0 &&
		len(r.DeprecatedPolicies) == 0 &&
		r.DeprecatedTTL == 0 &&
		r.DeprecatedMaxTTL == 0 &&
		r.DeprecatedPeriod == 0
}

func sameStrings(left, right []string) bool {
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}
