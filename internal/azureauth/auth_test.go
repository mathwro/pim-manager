package azureauth

import (
	"context"
	"errors"
	"testing"
)

func TestAccountReturnsCurrentAzureAccount(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az account show --output json": []byte(`{"id":"sub-1","tenantId":"tenant-1","user":{"name":"user@example.com"}}`),
	}}
	client := NewCLI(runner.Run)

	account, err := client.Account(context.Background())
	if err != nil {
		t.Fatalf("Account returned error: %v", err)
	}
	if account.SubscriptionID != "sub-1" || account.TenantID != "tenant-1" || account.UserName != "user@example.com" {
		t.Fatalf("unexpected account: %#v", account)
	}
}

func TestAccountReturnsLoginHintOnAzFailure(t *testing.T) {
	client := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("not logged in")
	})

	_, err := client.Account(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("expected ErrNotLoggedIn, got %v", err)
	}
}

func TestAccessTokenUsesRequestedResource(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az account get-access-token --resource https://graph.microsoft.com/ --output json": []byte(`{"accessToken":"abc","expiresOn":"2026-07-14 12:00:00.000000"}`),
	}}
	client := NewCLI(runner.Run)

	token, err := client.AccessToken(context.Background(), "https://graph.microsoft.com/")
	if err != nil {
		t.Fatalf("AccessToken returned error: %v", err)
	}
	if token != "abc" {
		t.Fatalf("expected token abc, got %q", token)
	}
}

func TestPrincipalIDUsesSignedInUser(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az ad signed-in-user show --query id --output tsv": []byte("principal-1\n"),
	}}
	client := NewCLI(runner.Run)

	principalID, err := client.PrincipalID(context.Background())
	if err != nil {
		t.Fatalf("PrincipalID returned error: %v", err)
	}
	if principalID != "principal-1" {
		t.Fatalf("expected principal-1, got %q", principalID)
	}
}

type fakeRunner struct {
	outputs map[string][]byte
}

func (f fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	out, ok := f.outputs[key]
	if !ok {
		return nil, errors.New("unexpected command: " + key)
	}
	return out, nil
}
