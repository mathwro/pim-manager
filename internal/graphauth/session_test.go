package graphauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
	"github.com/mathwro/pim-manager/internal/azureauth"
)

const (
	testTenant         = "11111111-1111-1111-1111-111111111111"
	testOtherTenant    = "22222222-2222-2222-2222-222222222222"
	testPrincipal      = "33333333-3333-3333-3333-333333333333"
	testOtherPrincipal = "44444444-4444-4444-4444-444444444444"
)

var testScopes = []string{
	"https://graph.microsoft.com/User.Read",
	"https://graph.microsoft.com/RoleEligibilitySchedule.Read.Directory",
	"https://graph.microsoft.com/RoleAssignmentSchedule.ReadWrite.Directory",
	"https://graph.microsoft.com/PrivilegedEligibilitySchedule.Read.AzureADGroup",
	"https://graph.microsoft.com/PrivilegedAssignmentSchedule.ReadWrite.AzureADGroup",
	"https://graph.microsoft.com/RoleManagementPolicy.Read.Directory",
	"https://graph.microsoft.com/RoleManagementPolicy.Read.AzureADGroup",
}

type fakeARM struct {
	check func(context.Context) (azureauth.ARMAuthentication, error)
}

func (f fakeARM) ARMAuthentication(ctx context.Context, mfa bool, acr string) (azureauth.ARMAuthentication, error) {
	if mfa || acr != "" {
		return azureauth.ARMAuthentication{}, errors.New("Graph authentication must not request ARM step-up")
	}
	if f.check != nil {
		return f.check(ctx)
	}
	return armIdentity(testTenant, testPrincipal), nil
}

type fakeMSAL struct {
	interactive func(context.Context, []string, string, string) (public.AuthResult, error)
	silent      func(context.Context, []string, public.Account, string) (public.AuthResult, error)
}

func (f *fakeMSAL) acquireInteractive(ctx context.Context, scopes []string, claims, hint string) (public.AuthResult, error) {
	return f.interactive(ctx, scopes, claims, hint)
}

func (f *fakeMSAL) acquireSilent(ctx context.Context, scopes []string, account public.Account, claims string) (public.AuthResult, error) {
	if f.silent == nil {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}
	return f.silent(ctx, scopes, account, claims)
}

func testSession(client *fakeMSAL, cli fakeARM) *Session {
	s := NewSession(cli)
	s.newClient = func(tenant string) (tokenClient, error) { return client, nil }
	return s
}

func tenantContext() context.Context {
	return azureauth.WithTenant(context.Background(), testTenant)
}

func jwtClaims(claims map[string]any) string {
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func armIdentity(tenant, principal string) azureauth.ARMAuthentication {
	return azureauth.ARMAuthentication{
		AccessToken: jwtClaims(map[string]any{"tid": tenant, "oid": principal}),
		PrincipalID: principal,
		Satisfied:   true,
	}
}

func graphIdentity(tenant, principal string, amr, acrs []string) public.AuthResult {
	result := public.AuthResult{
		AccessToken: jwtClaims(map[string]any{
			"tid": tenant, "oid": principal, "aud": "00000003-0000-0000-c000-000000000000",
			"exp": 4102444800, "amr": amr, "acrs": acrs,
			"scp": "User.Read RoleEligibilitySchedule.Read.Directory RoleAssignmentSchedule.ReadWrite.Directory PrivilegedEligibilitySchedule.Read.AzureADGroup PrivilegedAssignmentSchedule.ReadWrite.AzureADGroup RoleManagementPolicy.Read.Directory RoleManagementPolicy.Read.AzureADGroup",
		}),
		Account:       public.Account{HomeAccountID: "home.account", LocalAccountID: principal, Realm: tenant, PreferredUsername: "user@example.test"},
		GrantedScopes: append([]string(nil), testScopes...),
	}
	result.IDToken.Oid = principal
	result.IDToken.TenantID = tenant
	return result
}

// Omitting the tenant/principal checks would open a browser for an unintended identity.
func TestConnectRejectsInvalidARMIdentityBeforeBrowser(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		arm  azureauth.ARMAuthentication
	}{
		{"missing selected tenant", context.Background(), armIdentity(testTenant, testPrincipal)},
		{"wrong ARM tenant", tenantContext(), armIdentity(testOtherTenant, testPrincipal)},
		{"inconsistent ARM principal", tenantContext(), azureauth.ARMAuthentication{AccessToken: armIdentity(testTenant, testPrincipal).AccessToken, PrincipalID: testOtherPrincipal}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
				t.Fatal("browser opened before identity validation")
				return public.AuthResult{}, nil
			}}
			s := testSession(client, fakeARM{check: func(context.Context) (azureauth.ARMAuthentication, error) { return tc.arm, nil }})
			if err := s.Connect(tc.ctx); err == nil {
				t.Fatal("invalid ARM identity accepted")
			}
			if s.Connected(tc.ctx) {
				t.Fatal("invalid identity left session connected")
			}
		})
	}
}

func TestConnectRejectsGraphIdentityMismatch(t *testing.T) {
	for _, tc := range []struct{ name, tenant, principal string }{
		{"tenant", testOtherTenant, testPrincipal},
		{"principal", testTenant, testOtherPrincipal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSession(&fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
				return graphIdentity(tc.tenant, tc.principal, nil, nil), nil
			}}, fakeARM{})
			if err := s.Connect(tenantContext()); err == nil {
				t.Fatal("mismatched Graph identity accepted")
			}
			if s.Connected(tenantContext()) {
				t.Fatal("mismatched Graph identity retained")
			}
		})
	}
}

func TestConnectChecksARMIdentityAfterBrowser(t *testing.T) {
	principal := testPrincipal
	s := testSession(&fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		principal = testOtherPrincipal
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}, fakeARM{check: func(context.Context) (azureauth.ARMAuthentication, error) {
		return armIdentity(testTenant, principal), nil
	}})
	if err := s.Connect(tenantContext()); err == nil {
		t.Fatal("CLI drift during login accepted")
	}
	if s.Connected(tenantContext()) {
		t.Fatal("CLI drift retained session")
	}
}

func TestSessionReusesClientAndNeverBrowsesDuringAccessToken(t *testing.T) {
	interactive := 0
	silent := 0
	client := &fakeMSAL{
		interactive: func(_ context.Context, scopes []string, claims, _ string) (public.AuthResult, error) {
			interactive++
			if !reflect.DeepEqual(scopes, testScopes) || claims != "" {
				t.Fatalf("unexpected consent request: %v", scopes)
			}
			return graphIdentity(testTenant, testPrincipal, nil, nil), nil
		},
		silent: func(_ context.Context, scopes []string, account public.Account, claims string) (public.AuthResult, error) {
			silent++
			if account.LocalAccountID != testPrincipal || !reflect.DeepEqual(scopes, testScopes) || claims != "" {
				t.Fatal("silent acquisition lost connected account or scopes")
			}
			return graphIdentity(testTenant, testPrincipal, []string{"mfa"}, nil), nil
		},
	}
	s := testSession(client, fakeARM{})
	created := 0
	s.newClient = func(tenant string) (tokenClient, error) {
		created++
		if tenant != testTenant {
			t.Fatal("client authority is not selected tenant")
		}
		return client, nil
	}
	ctx := tenantContext()
	if _, err := s.AccessToken(ctx, "https://graph.microsoft.com"); err == nil {
		t.Fatal("unconnected token access succeeded")
	}
	if err := s.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	token, err := s.AccessToken(ctx, "https://graph.microsoft.com/")
	if err != nil || token == "" {
		t.Fatalf("silent token: %q, %v", token, err)
	}
	if interactive != 1 || created != 1 || silent != 2 {
		t.Fatalf("acquisitions: browser=%d clients=%d silent=%d", interactive, created, silent)
	}
	client.silent = func(context.Context, []string, public.Account, string) (public.AuthResult, error) {
		return public.AuthResult{}, errors.New("interaction_required")
	}
	if _, err := s.AccessToken(ctx, "https://graph.microsoft.com"); err == nil {
		t.Fatal("silent failure accepted")
	}
	if interactive != 1 {
		t.Fatal("token access opened a browser")
	}
	if s.Connected(ctx) {
		t.Fatal("silent acquisition failure did not require reconnect")
	}
}

func TestAccessTokenRejectsOtherResourcesAndTenantDrift(t *testing.T) {
	client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccessToken(tenantContext(), "https://management.azure.com"); err == nil {
		t.Fatal("Graph credentials accepted for another audience")
	}
	if _, err := s.AccessToken(azureauth.WithTenant(context.Background(), testOtherTenant), "https://graph.microsoft.com"); err == nil {
		t.Fatal("selected tenant drift accepted")
	}
	if !s.Connected(tenantContext()) {
		t.Fatal("mismatched caller invalidated the current tenant session")
	}
}

func TestAuthenticationPinsSatisfiedTokenWithoutBrowser(t *testing.T) {
	checked := graphIdentity(testTenant, testPrincipal, []string{"mfa"}, []string{"c1"})
	client := &fakeMSAL{
		interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
			return graphIdentity(testTenant, testPrincipal, nil, nil), nil
		},
		silent: func(context.Context, []string, public.Account, string) (public.AuthResult, error) {
			return checked, nil
		},
	}
	s := testSession(client, fakeARM{})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	client.interactive = func(context.Context, []string, string, string) (public.AuthResult, error) {
		t.Fatal("satisfied policy opened browser")
		return public.AuthResult{}, nil
	}
	auth, err := s.Authentication(tenantContext(), true, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.Satisfied || auth.PrincipalID != testPrincipal || auth.AccessToken != checked.AccessToken {
		t.Fatal("preflight did not return exact checked identity/token")
	}
}

func TestAuthenticationRequestsMFAAndContextClaimsAndRejectsStepUpDrift(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "principal drift"}[drift], func(t *testing.T) {
			client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
				return graphIdentity(testTenant, testPrincipal, nil, nil), nil
			}}
			s := testSession(client, fakeARM{})
			if err := s.Connect(tenantContext()); err != nil {
				t.Fatal(err)
			}
			client.silent = func(_ context.Context, _ []string, _ public.Account, claims string) (public.AuthResult, error) {
				if claims != "" {
					return public.AuthResult{}, errors.New("interaction_required")
				}
				return graphIdentity(testTenant, testPrincipal, nil, nil), nil
			}
			stepped := graphIdentity(testTenant, testPrincipal, []string{"mfa"}, []string{"c1"})
			if drift {
				stepped = graphIdentity(testTenant, testOtherPrincipal, []string{"mfa"}, []string{"c1"})
			}
			client.interactive = func(_ context.Context, _ []string, claims, hint string) (public.AuthResult, error) {
				var got any
				var want any
				if err := json.Unmarshal([]byte(claims), &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(`{"access_token":{"amr":{"essential":true,"values":["mfa"]},"acrs":{"essential":true,"value":"c1"}}}`), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) || hint != "user@example.test" {
					t.Fatalf("incorrect claims request/hint: %s %q", claims, hint)
				}
				return stepped, nil
			}
			auth, err := s.Authentication(tenantContext(), true, "c1")
			if drift {
				if err == nil || auth.Satisfied || auth.AccessToken != "" || s.Connected(tenantContext()) {
					t.Fatal("step-up identity drift accepted")
				}
			} else if err != nil || !auth.Satisfied || auth.AccessToken != stepped.AccessToken {
				t.Fatalf("step-up result: %#v %v", auth, err)
			}
		})
	}
}

func TestAuthenticationRejectsCLIPrincipalDriftBeforeStepUp(t *testing.T) {
	principal := testPrincipal
	client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{check: func(context.Context) (azureauth.ARMAuthentication, error) {
		return armIdentity(testTenant, principal), nil
	}})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	principal = testOtherPrincipal
	client.silent = func(context.Context, []string, public.Account, string) (public.AuthResult, error) {
		t.Fatal("token requested after CLI principal drift")
		return public.AuthResult{}, nil
	}
	if auth, err := s.Authentication(tenantContext(), false, ""); err == nil || auth.AccessToken != "" {
		t.Fatal("CLI drift accepted")
	}
}

func TestResetCancelsBrowserAndFencesStaleResult(t *testing.T) {
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	client := &fakeMSAL{interactive: func(ctx context.Context, _ []string, _, _ string) (public.AuthResult, error) {
		entered <- ctx
		<-release
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{})
	result := make(chan error, 1)
	go func() { result <- s.Connect(tenantContext()) }()
	browserContext := <-entered
	s.Reset()
	select {
	case <-browserContext.Done():
	default:
		t.Fatal("reset did not cancel browser context")
	}
	fresh := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s.newClient = func(string) (tokenClient, error) { return fresh, nil }
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("stale browser result accepted")
	}
	if !s.Connected(tenantContext()) {
		t.Fatal("stale result cleared fresh connection")
	}
}

func TestCancellationDiscardsSuccessfulBrowserResult(t *testing.T) {
	ctx, cancel := context.WithCancel(tenantContext())
	s := testSession(&fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		cancel()
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}, fakeARM{})
	if err := s.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error lost: %v", err)
	}
	if s.Connected(tenantContext()) {
		t.Fatal("canceled login retained session")
	}
}

func TestConcurrentSilentAcquisitionsCanBeReset(t *testing.T) {
	client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	client.silent = func(ctx context.Context, _ []string, _ public.Account, _ string) (public.AuthResult, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return public.AuthResult{}, ctx.Err()
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AccessToken(tenantContext(), "https://graph.microsoft.com"); err == nil {
				t.Error("canceled silent result accepted")
			}
		}()
	}
	<-entered
	<-entered
	s.Reset()
	wg.Wait()
	if s.Connected(tenantContext()) {
		t.Fatal("reset retained session")
	}
}

// A section switch cancels the old discovery's read; the shared session and the new read must survive.
func TestCanceledReadKeepsSharedSession(t *testing.T) {
	client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	client.silent = func(ctx context.Context, _ []string, _ public.Account, _ string) (public.AuthResult, error) {
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return public.AuthResult{}, ctx.Err()
		case <-release:
			return graphIdentity(testTenant, testPrincipal, nil, nil), nil
		}
	}
	oldContext, cancelOld := context.WithCancel(tenantContext())
	oldResult, newResult := make(chan error, 1), make(chan error, 1)
	go func() { _, err := s.AccessToken(oldContext, "https://graph.microsoft.com"); oldResult <- err }()
	go func() { _, err := s.AccessToken(tenantContext(), "https://graph.microsoft.com"); newResult <- err }()
	<-entered
	<-entered
	cancelOld()
	if err := <-oldResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	close(release)
	if err := <-newResult; err != nil {
		t.Fatalf("canceled read aborted another section's read: %v", err)
	}
	if !s.Connected(tenantContext()) {
		t.Fatal("canceled read disconnected the shared session")
	}
}

func TestAuthenticationErrorsPreserveCodesWithoutSecrets(t *testing.T) {
	client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return public.AuthResult{}, errors.New(`AADSTS53003 blocked access_token=secret-token claims={"acrs":"secret-context"}`)
	}}
	s := testSession(client, fakeARM{})
	err := s.Connect(tenantContext())
	if err == nil || !strings.Contains(err.Error(), "AADSTS53003") {
		t.Fatalf("actionable error code lost: %v", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "acrs") {
		t.Fatalf("credential/claims leaked: %v", err)
	}
}

func TestBrowserCommandUsesWindowsHostUnderWSL(t *testing.T) {
	name, args := browserCommand("linux", true, "https://example.test/?q=it's")
	if name != "powershell.exe" || !strings.Contains(strings.Join(args, " "), "Start-Process") || !strings.Contains(strings.Join(args, " "), "it''s") {
		t.Fatalf("WSL browser command: %s %v", name, args)
	}
	for _, tc := range []struct{ platform, executable string }{{"windows", "rundll32"}, {"darwin", "open"}, {"linux", "xdg-open"}} {
		name, _ := browserCommand(tc.platform, false, "https://example.test")
		if name != tc.executable {
			t.Fatalf("%s browser executable %q", tc.platform, name)
		}
	}
}

func TestConnectRejectsIncompleteGraphEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*public.AuthResult)
	}{
		{"opaque token", func(r *public.AuthResult) { r.AccessToken = "opaque" }},
		{"missing ID token identity", func(r *public.AuthResult) { r.IDToken.Oid = "" }},
		{"account principal mismatch", func(r *public.AuthResult) { r.Account.LocalAccountID = testOtherPrincipal }},
		{"account tenant mismatch", func(r *public.AuthResult) { r.Account.Realm = testOtherTenant }},
		{"missing cache account", func(r *public.AuthResult) { r.Account.HomeAccountID = "" }},
		{"missing consent", func(r *public.AuthResult) { r.GrantedScopes = []string{"User.Read"} }},
		{"missing delegated token scopes", func(r *public.AuthResult) {
			r.AccessToken = jwtClaims(map[string]any{"tid": testTenant, "oid": testPrincipal, "aud": "https://graph.microsoft.com", "exp": 4102444800})
		}},
		{"wrong audience", func(r *public.AuthResult) {
			r.AccessToken = jwtClaims(map[string]any{"tid": testTenant, "oid": testPrincipal, "aud": "https://management.azure.com", "exp": 4102444800})
		}},
		{"expired token", func(r *public.AuthResult) {
			r.AccessToken = jwtClaims(map[string]any{"tid": testTenant, "oid": testPrincipal, "aud": "https://graph.microsoft.com", "exp": 1})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := graphIdentity(testTenant, testPrincipal, nil, nil)
			tc.mutate(&result)
			s := testSession(&fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) { return result, nil }}, fakeARM{})
			if err := s.Connect(tenantContext()); err == nil {
				t.Fatal("incomplete token evidence accepted")
			}
		})
	}
}

func TestAuthenticationUsesSilentClaimsRefreshWhenAvailable(t *testing.T) {
	client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	client.interactive = func(context.Context, []string, string, string) (public.AuthResult, error) {
		t.Fatal("silent claims refresh unnecessarily opened browser")
		return public.AuthResult{}, nil
	}
	stepped := graphIdentity(testTenant, testPrincipal, []string{"mfa"}, nil)
	client.silent = func(_ context.Context, _ []string, _ public.Account, claims string) (public.AuthResult, error) {
		if claims == "" {
			return graphIdentity(testTenant, testPrincipal, nil, nil), nil
		}
		return stepped, nil
	}
	auth, err := s.Authentication(tenantContext(), true, "")
	if err != nil || !auth.Satisfied || auth.AccessToken != stepped.AccessToken {
		t.Fatalf("silent step-up: %#v %v", auth, err)
	}
}

func TestAuthenticationRejectsUnfulfilledClaims(t *testing.T) {
	client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	auth, err := s.Authentication(tenantContext(), true, "c1")
	if err == nil || auth.Satisfied || auth.AccessToken != "" {
		t.Fatal("unsatisfied claims marked satisfied")
	}
}

func TestAuthenticationChecksCLIAfterInteractiveStepUp(t *testing.T) {
	principal := testPrincipal
	client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{check: func(context.Context) (azureauth.ARMAuthentication, error) {
		return armIdentity(testTenant, principal), nil
	}})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	client.interactive = func(context.Context, []string, string, string) (public.AuthResult, error) {
		principal = testOtherPrincipal
		return graphIdentity(testTenant, testPrincipal, []string{"mfa"}, nil), nil
	}
	auth, err := s.Authentication(tenantContext(), true, "")
	if err == nil || auth.AccessToken != "" || s.Connected(tenantContext()) {
		t.Fatal("CLI drift during step-up accepted")
	}
}

func TestAuthenticationChecksCLIBeforeOpeningStepUpBrowser(t *testing.T) {
	principal := testPrincipal
	client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{check: func(context.Context) (azureauth.ARMAuthentication, error) {
		return armIdentity(testTenant, principal), nil
	}})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	client.silent = func(_ context.Context, _ []string, _ public.Account, claims string) (public.AuthResult, error) {
		if claims != "" {
			principal = testOtherPrincipal
			return public.AuthResult{}, errors.New("interaction_required")
		}
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}
	client.interactive = func(context.Context, []string, string, string) (public.AuthResult, error) {
		t.Fatal("browser opened after CLI principal drift")
		return public.AuthResult{}, nil
	}
	if auth, err := s.Authentication(tenantContext(), true, ""); err == nil || auth.AccessToken != "" {
		t.Fatal("CLI drift before browser accepted")
	}
}

func TestMissingTenantPreservesConnectedSession(t *testing.T) {
	s := testSession(&fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}, fakeARM{})
	if err := s.Connect(tenantContext()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccessToken(context.Background(), "https://graph.microsoft.com"); err == nil {
		t.Fatal("missing tenant accepted")
	}
	if !s.Connected(tenantContext()) {
		t.Fatal("missing tenant invalidated the current session")
	}
}

func TestStaleCallersCannotResetNewTenantSession(t *testing.T) {
	for _, kind := range []string{"canceled stale tenant", "canceled current tenant", "missing tenant", "mismatched tenant"} {
		for _, method := range []string{"AccessToken", "Authentication", "Connect", "Connected"} {
			t.Run(kind+"/"+method, func(t *testing.T) {
				newContext := azureauth.WithTenant(context.Background(), testOtherTenant)
				client := &fakeMSAL{interactive: func(context.Context, []string, string, string) (public.AuthResult, error) {
					return graphIdentity(testOtherTenant, testPrincipal, nil, nil), nil
				}}
				s := testSession(client, fakeARM{check: func(ctx context.Context) (azureauth.ARMAuthentication, error) {
					return armIdentity(azureauth.TenantFromContext(ctx), testPrincipal), nil
				}})
				if err := s.Connect(newContext); err != nil {
					t.Fatal(err)
				}
				client.interactive = func(context.Context, []string, string, string) (public.AuthResult, error) {
					t.Fatal("stale caller opened a browser")
					return public.AuthResult{}, nil
				}
				client.silent = func(context.Context, []string, public.Account, string) (public.AuthResult, error) {
					t.Fatal("stale caller requested a token")
					return public.AuthResult{}, nil
				}
				oldContext := tenantContext()
				switch kind {
				case "canceled stale tenant", "canceled current tenant":
					if kind == "canceled current tenant" {
						oldContext = newContext
					}
					canceled, cancel := context.WithCancel(oldContext)
					cancel()
					oldContext = canceled
				case "missing tenant":
					oldContext = context.Background()
				}
				var err error
				switch method {
				case "AccessToken":
					_, err = s.AccessToken(oldContext, "https://graph.microsoft.com")
				case "Authentication":
					_, err = s.Authentication(oldContext, false, "")
				case "Connect":
					err = s.Connect(oldContext)
				case "Connected":
					if s.Connected(oldContext) {
						t.Fatal("stale caller reported connected")
					}
				}
				if method != "Connected" && err == nil {
					t.Fatal("stale caller was accepted")
				}
				if !s.Connected(newContext) {
					t.Fatal("stale caller destroyed the new tenant connection")
				}
			})
		}
	}
}

func TestMismatchedConnectCannotCancelCurrentLogin(t *testing.T) {
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	client := &fakeMSAL{interactive: func(ctx context.Context, _ []string, _, _ string) (public.AuthResult, error) {
		entered <- ctx
		<-release
		return graphIdentity(testTenant, testPrincipal, nil, nil), nil
	}}
	s := testSession(client, fakeARM{})
	s.newClient = func(tenant string) (tokenClient, error) {
		if tenant != testTenant {
			return nil, errors.New("unexpected stale tenant")
		}
		return client, nil
	}
	result := make(chan error, 1)
	go func() { result <- s.Connect(tenantContext()) }()
	loginContext := <-entered
	if err := s.Connect(azureauth.WithTenant(context.Background(), testOtherTenant)); err == nil {
		t.Error("mismatched Connect accepted during login")
	}
	if loginContext.Err() != nil {
		t.Error("mismatched Connect canceled the current login")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if !s.Connected(tenantContext()) {
		t.Fatal("current login was not retained")
	}
}

func TestConnectAndStepUpUseCheckedARMUsernameHint(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		usernames               map[string]any
		wantInitial, wantStepUp string
	}{
		{"preferred username", map[string]any{"preferred_username": " preferred@example.test ", "upn": "upn@example.test", "unique_name": "unique@example.test"}, "preferred@example.test", "preferred@example.test"},
		{"UPN fallback", map[string]any{"upn": "upn@example.test", "unique_name": "unique@example.test"}, "upn@example.test", "upn@example.test"},
		{"unique name fallback", map[string]any{"unique_name": "unique@example.test"}, "unique@example.test", "unique@example.test"},
		{"no CLI username", nil, "", "graph-account@example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arm := armIdentity(testTenant, testPrincipal)
			claims := map[string]any{"tid": testTenant, "oid": testPrincipal}
			for key, value := range tc.usernames {
				claims[key] = value
			}
			arm.AccessToken = jwtClaims(claims)
			client := &fakeMSAL{interactive: func(_ context.Context, _ []string, challenge, hint string) (public.AuthResult, error) {
				want := tc.wantInitial
				var methods []string
				if challenge != "" {
					want = tc.wantStepUp
					methods = []string{"mfa"}
				}
				if hint != want {
					t.Fatalf("browser login hint %q, want %q", hint, want)
				}
				result := graphIdentity(testTenant, testPrincipal, methods, nil)
				result.Account.PreferredUsername = "graph-account@example.test"
				return result, nil
			}}
			s := testSession(client, fakeARM{check: func(context.Context) (azureauth.ARMAuthentication, error) { return arm, nil }})
			if err := s.Connect(tenantContext()); err != nil {
				t.Fatal(err)
			}
			client.silent = func(_ context.Context, _ []string, _ public.Account, claims string) (public.AuthResult, error) {
				if claims != "" {
					return public.AuthResult{}, errors.New("interaction_required")
				}
				return graphIdentity(testTenant, testPrincipal, nil, nil), nil
			}
			if auth, err := s.Authentication(tenantContext(), true, ""); err != nil || !auth.Satisfied {
				t.Fatalf("step-up result: %#v %v", auth, err)
			}
		})
	}
}
