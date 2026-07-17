package azureauth

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"slices"
	"strings"
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

func TestAccountReturnsContextErrorsUnchanged(t *testing.T) {
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(contextErr.Error(), func(t *testing.T) {
			client := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
				return nil, contextErr
			})

			_, err := client.Account(context.Background())
			if !errors.Is(err, contextErr) {
				t.Fatalf("expected %v, got %v", contextErr, err)
			}
			if errors.Is(err, ErrNotLoggedIn) {
				t.Fatalf("did not expect ErrNotLoggedIn, got %v", err)
			}
		})
	}
}

func TestAccountWrapsUnexpectedAzFailureWithLoginHint(t *testing.T) {
	commandErr := errors.New("az account show failed")
	client := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
		return nil, commandErr
	})

	_, err := client.Account(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("expected ErrNotLoggedIn, got %v", err)
	}
	if !strings.Contains(err.Error(), commandErr.Error()) {
		t.Fatalf("expected command error details, got %v", err)
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

func TestStepUpLoginCommandUsesTenantARMAndMFAClaim(t *testing.T) {
	command, err := StepUpLoginCommand(" tenant-1 ", true, "")
	if err != nil {
		t.Fatalf("StepUpLoginCommand returned error: %v", err)
	}
	claims := base64.StdEncoding.EncodeToString([]byte(`{"access_token":{"amr":{"essential":true,"values":["mfa"]}}}`))
	want := []string{
		"az", "login",
		"--tenant", "tenant-1",
		"--scope", "https://management.core.windows.net//.default",
		"--claims-challenge", claims,
		"--output", "none",
	}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("expected args %#v, got %#v", want, command.Args)
	}
	if !slices.Contains(command.Env, "AZURE_CORE_LOGIN_EXPERIENCE_V2=off") {
		t.Fatalf("expected subscription selector disabled for child login, env=%#v", command.Env)
	}
}

func TestStepUpLoginCommandUsesAuthenticationContext(t *testing.T) {
	command, err := StepUpLoginCommand("tenant-1", false, " c1 ")
	if err != nil {
		t.Fatalf("StepUpLoginCommand returned error: %v", err)
	}
	claims := base64.StdEncoding.EncodeToString([]byte(`{"access_token":{"acrs":{"essential":true,"value":"c1"}}}`))
	if got := command.Args[7]; got != claims {
		t.Fatalf("expected authentication context claims %q, got %q", claims, got)
	}
}

func TestStepUpLoginCommandCombinesMFAAndAuthenticationContext(t *testing.T) {
	command, err := StepUpLoginCommand("tenant-1", true, "c1")
	if err != nil {
		t.Fatalf("StepUpLoginCommand returned error: %v", err)
	}
	claims := base64.StdEncoding.EncodeToString([]byte(`{"access_token":{"acrs":{"essential":true,"value":"c1"},"amr":{"essential":true,"values":["mfa"]}}}`))
	if got := command.Args[7]; got != claims {
		t.Fatalf("expected combined claims %q, got %q", claims, got)
	}
}

func TestStepUpLoginCommandRejectsMissingTenant(t *testing.T) {
	if _, err := StepUpLoginCommand("  ", true, ""); err == nil {
		t.Fatal("expected missing tenant error")
	}
}

func TestARMAuthenticationUsesExistingTokenClaims(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"oid":"principal-1","amr":["pwd","mfa"],"acrs":["c1"]}`))
	runner := fakeRunner{outputs: map[string][]byte{
		"az account get-access-token --resource https://management.core.windows.net/ --output json": []byte(`{"accessToken":"e30.` + payload + `.signature"}`),
	}}
	client := NewCLI(runner.Run)

	authentication, err := client.ARMAuthentication(context.Background(), true, "c1")
	if err != nil {
		t.Fatalf("ARMAuthentication returned error: %v", err)
	}
	if authentication.PrincipalID != "principal-1" || !authentication.Satisfied || authentication.AccessToken == "" {
		t.Fatalf("expected checked token for principal-1 with satisfied MFA and c1, got %#v", authentication)
	}

	authentication, err = client.ARMAuthentication(context.Background(), true, "c2")
	if err != nil || authentication.Satisfied {
		t.Fatalf("expected missing c2 to require step-up, authentication=%#v error=%v", authentication, err)
	}

	missingMFAPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"oid":"principal-1","amr":["pwd"]}`))
	client = NewCLI(fakeRunner{outputs: map[string][]byte{
		"az account get-access-token --resource https://management.core.windows.net/ --output json": []byte(`{"accessToken":"e30.` + missingMFAPayload + `.signature"}`),
	}}.Run)
	authentication, err = client.ARMAuthentication(context.Background(), true, "")
	if err != nil || !authentication.Satisfied {
		t.Fatalf("expected MFA-only activation to reuse the current token, authentication=%#v error=%v", authentication, err)
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
