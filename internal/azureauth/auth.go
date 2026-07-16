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
)

var ErrNotLoggedIn = errors.New("azure cli is not logged in; run az login")

type Runner func(context.Context, string, ...string) ([]byte, error)

type CLI struct {
	run Runner
}

type Account struct {
	SubscriptionID string
	TenantID       string
	UserName       string
}

func NewCLI(run Runner) CLI {
	if run == nil {
		run = execCommand
	}
	return CLI{run: run}
}

func (c CLI) Account(ctx context.Context) (Account, error) {
	out, err := c.run(ctx, "az", "account", "show", "--output", "json")
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Account{}, err
		}
		return Account{}, fmt.Errorf("%w: %v", ErrNotLoggedIn, err)
	}

	var payload struct {
		ID       string `json:"id"`
		TenantID string `json:"tenantId"`
		User     struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return Account{}, fmt.Errorf("parse az account output: %w", err)
	}
	return Account{SubscriptionID: payload.ID, TenantID: payload.TenantID, UserName: payload.User.Name}, nil
}

func (c CLI) AccessToken(ctx context.Context, resource string) (string, error) {
	out, err := c.run(ctx, "az", "account", "get-access-token", "--resource", resource, "--output", "json")
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
