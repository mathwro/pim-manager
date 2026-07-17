package azureauth

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTenantsReturnsDistinctAzureTenants(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az account tenant list --output json": []byte(`[
			{"tenantId":"tenant-1","displayName":"Contoso","defaultDomain":"contoso.onmicrosoft.com"},
			{"tenantId":"tenant-2","defaultDomain":"fabrikam.onmicrosoft.com"},
			{"tenantId":"tenant-1","displayName":"duplicate"},
			{"tenantId":"  "}
		]`),
	}}

	tenants, err := NewCLI(runner.Run).Tenants(context.Background())
	if err != nil {
		t.Fatalf("Tenants returned error: %v", err)
	}
	want := []Tenant{
		{ID: "tenant-1", DisplayName: "Contoso", DefaultDomain: "contoso.onmicrosoft.com"},
		{ID: "tenant-2", DefaultDomain: "fabrikam.onmicrosoft.com"},
	}
	if !reflect.DeepEqual(tenants, want) {
		t.Fatalf("expected %#v, got %#v", want, tenants)
	}
}

func TestTenantsReturnsLoginHintWhenNoneAreUsable(t *testing.T) {
	client := NewCLI(fakeRunner{outputs: map[string][]byte{
		"az account tenant list --output json": []byte(`[{"tenantId":" "}]`),
	}}.Run)

	_, err := client.Tenants(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("expected ErrNotLoggedIn, got %v", err)
	}
}

func TestTenantsPreservesContextErrors(t *testing.T) {
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(contextErr.Error(), func(t *testing.T) {
			client := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
				return nil, contextErr
			})
			_, err := client.Tenants(context.Background())
			if !errors.Is(err, contextErr) || errors.Is(err, ErrNotLoggedIn) {
				t.Fatalf("expected unchanged %v, got %v", contextErr, err)
			}
		})
	}
}

func TestTenantsClassifiesAzureCLILoginFailure(t *testing.T) {
	commandErr := errors.New("exit status 1")
	client := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Please run 'az login' to setup account."), commandErr
	})

	_, err := client.Tenants(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) || !strings.Contains(err.Error(), "Please run 'az login'") {
		t.Fatalf("expected login failure with Azure CLI details, got %v", err)
	}
}

func TestTenantsPreservesNonLoginAzureCLIError(t *testing.T) {
	commandErr := errors.New("exit status 1")
	client := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("network unavailable"), commandErr
	})

	_, err := client.Tenants(context.Background())
	if errors.Is(err, ErrNotLoggedIn) || !strings.Contains(err.Error(), "network unavailable") || !errors.Is(err, commandErr) {
		t.Fatalf("expected non-login error with Azure CLI details, got %v", err)
	}
}

func TestTenantsRejectsInvalidJSON(t *testing.T) {
	client := NewCLI(fakeRunner{outputs: map[string][]byte{
		"az account tenant list --output json": []byte(`not-json`),
	}}.Run)
	if _, err := client.Tenants(context.Background()); err == nil || !strings.Contains(err.Error(), "parse az account tenant list output") {
		t.Fatalf("expected parse error, got %v", err)
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

func TestAccessTokenUsesTenantFromContext(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az account get-access-token --resource https://management.core.windows.net/ --tenant tenant-1 --output json": []byte(`{"accessToken":"abc"}`),
	}}
	ctx := WithTenant(context.Background(), " tenant-1 ")

	token, err := NewCLI(runner.Run).AccessToken(ctx, "https://management.core.windows.net/")
	if err != nil || token != "abc" {
		t.Fatalf("expected tenant-scoped token abc, got %q, %v", token, err)
	}
	if got := TenantFromContext(ctx); got != "tenant-1" {
		t.Fatalf("expected tenant-1 in context, got %q", got)
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

func TestExecCommandSeparatesStdoutFromSuccessfulStderr(t *testing.T) {
	if os.Args[len(os.Args)-1] == "helper-success" {
		_, _ = os.Stdout.WriteString(`{"tenantId":"tenant-1"}`)
		_, _ = os.Stderr.WriteString("Azure CLI warning")
		os.Exit(0)
	}

	out, err := execCommand(context.Background(), os.Args[0], "-test.run=^TestExecCommandSeparatesStdoutFromSuccessfulStderr$", "--", "helper-success")
	if err != nil {
		t.Fatalf("execCommand returned error: %v", err)
	}
	if got := string(out); got != `{"tenantId":"tenant-1"}` {
		t.Fatalf("expected clean stdout JSON, got %q", got)
	}
}

func TestAzureCLIErrorIncludesExitStderr(t *testing.T) {
	if os.Args[len(os.Args)-1] == "helper-failure" {
		_, _ = os.Stderr.WriteString("network unavailable")
		os.Exit(3)
	}

	out, err := execCommand(context.Background(), os.Args[0], "-test.run=^TestAzureCLIErrorIncludesExitStderr$", "--", "helper-failure")
	if err == nil {
		t.Fatal("expected helper failure")
	}
	if detailed := azureCLIError(out, err); !strings.Contains(detailed.Error(), "network unavailable") {
		t.Fatalf("expected stderr details, got %v", detailed)
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
