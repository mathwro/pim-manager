package azureauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mathwro/pim-manager/internal/arm"
)

var ErrNotLoggedIn = errors.New("azure cli is not logged in; run az login")

const tenantMetadataQuery = "[].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain}"

type Runner func(context.Context, string, ...string) ([]byte, error)

type CLI struct {
	run Runner
}

type Tenant struct {
	ID            string
	DisplayName   string
	DefaultDomain string
}

func NewCLI(run Runner) CLI {
	if run == nil {
		run = execCommand
	}
	return CLI{run: run}
}

type tenantContextKey struct{}

func WithTenant(ctx context.Context, tenantID string) context.Context {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantContextKey{}, tenantID)
}

func TenantFromContext(ctx context.Context) string {
	tenantID, _ := ctx.Value(tenantContextKey{}).(string)
	return tenantID
}

func azureCLIError(out []byte, err error) error {
	details := strings.TrimSpace(string(out))
	if details == "" {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			details = strings.TrimSpace(string(exitErr.Stderr))
		}
	}
	if details == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, details)
}

func azureCLILoginRequired(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "az login") || strings.Contains(message, "not logged in")
}

func (c CLI) Tenants(ctx context.Context) ([]Tenant, error) {
	out, err := c.run(ctx, "az", "account", "tenant", "list", "--output", "json")
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		commandErr := azureCLIError(out, err)
		if azureCLILoginRequired(commandErr) {
			return nil, fmt.Errorf("%w: %w", ErrNotLoggedIn, commandErr)
		}
		return nil, fmt.Errorf("list Azure CLI tenants: %w", commandErr)
	}

	var payload []struct {
		TenantID      string `json:"tenantId"`
		DisplayName   string `json:"displayName"`
		DefaultDomain string `json:"defaultDomain"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("parse az account tenant list output: %w", err)
	}

	tenants := make([]Tenant, 0, len(payload))
	seen := make(map[string]struct{}, len(payload))
	for _, item := range payload {
		id := strings.TrimSpace(item.TenantID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		tenants = append(tenants, Tenant{
			ID:            id,
			DisplayName:   strings.TrimSpace(item.DisplayName),
			DefaultDomain: strings.TrimSpace(item.DefaultDomain),
		})
	}
	if len(tenants) == 0 {
		return nil, fmt.Errorf("%w: Azure CLI returned no tenants", ErrNotLoggedIn)
	}
	needsMetadata := false
	for _, tenant := range tenants {
		if tenant.DisplayName == "" && tenant.DefaultDomain == "" {
			needsMetadata = true
			break
		}
	}
	if !needsMetadata {
		return tenants, nil
	}

	out, err = c.run(ctx, "az", "account", "list", "--all", "--query", tenantMetadataQuery, "--output", "json")
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("list Azure CLI accounts: %w", azureCLIError(out, err))
	}
	var accounts []struct {
		TenantID      string `json:"tenantId"`
		DisplayName   string `json:"displayName"`
		DefaultDomain string `json:"defaultDomain"`
	}
	if err := json.Unmarshal(out, &accounts); err != nil {
		return nil, fmt.Errorf("parse az account list output: %w", err)
	}
	indexes := make(map[string]int, len(tenants))
	for index, tenant := range tenants {
		indexes[strings.ToLower(tenant.ID)] = index
	}
	for _, account := range accounts {
		index, ok := indexes[strings.ToLower(strings.TrimSpace(account.TenantID))]
		if !ok {
			continue
		}
		if tenants[index].DisplayName == "" {
			tenants[index].DisplayName = strings.TrimSpace(account.DisplayName)
		}
		if tenants[index].DefaultDomain == "" {
			tenants[index].DefaultDomain = strings.TrimSpace(account.DefaultDomain)
		}
	}
	return tenants, nil
}

func (c CLI) AccessToken(ctx context.Context, resource string) (string, error) {
	args := []string{"account", "get-access-token", "--resource", resource}
	if tenantID := TenantFromContext(ctx); tenantID != "" {
		args = append(args, "--tenant", tenantID)
	}
	args = append(args, "--output", "json")
	out, err := c.run(ctx, "az", args...)
	if err != nil {
		return "", fmt.Errorf("get Azure CLI access token for %s: %w", resource, err)
	}
	var payload struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", fmt.Errorf("parse Azure CLI access token output: %w", err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("Azure CLI returned empty access token for %s", resource)
	}
	return payload.AccessToken, nil
}

type ARMAuthentication struct {
	AccessToken string
	PrincipalID string
	Satisfied   bool
}

func (c CLI) ARMAuthentication(ctx context.Context, mfaRequired bool, authenticationContext string) (ARMAuthentication, error) {
	token, err := c.AccessToken(ctx, arm.Resource)
	if err != nil {
		return ARMAuthentication{}, err
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ARMAuthentication{}, errors.New("parse Azure CLI ARM token: invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ARMAuthentication{}, fmt.Errorf("parse Azure CLI ARM token claims: %w", err)
	}
	var claims struct {
		PrincipalID            string   `json:"oid"`
		AuthenticationMethods  []string `json:"amr"`
		AuthenticationContexts []string `json:"acrs"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ARMAuthentication{}, fmt.Errorf("parse Azure CLI ARM token claims: %w", err)
	}
	if claims.PrincipalID == "" {
		return ARMAuthentication{}, errors.New("Azure CLI ARM token has no principal ID")
	}
	// ARM/PIM remains the authority for standard MFA; only authentication contexts require preflight step-up.
	satisfied := true
	authenticationContext = strings.TrimSpace(authenticationContext)
	if authenticationContext != "" {
		satisfied = satisfied && contains(claims.AuthenticationContexts, authenticationContext)
	}
	return ARMAuthentication{AccessToken: token, PrincipalID: claims.PrincipalID, Satisfied: satisfied}, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (c CLI) PrincipalID(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "az", "ad", "signed-in-user", "show", "--query", "id", "--output", "tsv")
	if err != nil {
		return "", fmt.Errorf("get signed-in user principal ID: %w", err)
	}
	principalID := strings.TrimSpace(string(out))
	if principalID == "" {
		return "", errors.New("Azure CLI returned empty signed-in user principal ID")
	}
	return principalID, nil
}

func StepUpLoginCommand(tenantID string, mfaRequired bool, authenticationContext string) (*exec.Cmd, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, errors.New("Azure tenant ID is required for step-up authentication")
	}
	authenticationContext = strings.TrimSpace(authenticationContext)
	accessTokenClaims := map[string]any{}
	if mfaRequired {
		accessTokenClaims["amr"] = map[string]any{"essential": true, "values": []string{"mfa"}}
	}
	if authenticationContext != "" {
		accessTokenClaims["acrs"] = map[string]any{"essential": true, "value": authenticationContext}
	}
	if len(accessTokenClaims) == 0 {
		return nil, errors.New("MFA or an authentication context is required for step-up authentication")
	}
	claimsJSON, err := json.Marshal(map[string]any{"access_token": accessTokenClaims})
	if err != nil {
		return nil, fmt.Errorf("encode step-up claims: %w", err)
	}
	claims := base64.StdEncoding.EncodeToString(claimsJSON)
	command := exec.Command(
		"az", "login",
		"--tenant", tenantID,
		"--scope", "https://management.core.windows.net//.default",
		"--claims-challenge", claims,
		"--output", "none",
	)
	command.Env = append(os.Environ(), "AZURE_CORE_LOGIN_EXPERIENCE_V2=off")
	return command, nil
}

func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
