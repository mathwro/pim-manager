package azureauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
