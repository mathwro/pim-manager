package azureauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mathwro/pim-manager/internal/arm"
)

func TestCheckAccountFindsLiveTenantScopedSelf(t *testing.T) {
	for _, token := range []string{"opaque-graph-token", graphToken(`{"tid":"TENANT-1","oid":"PRINCIPAL-1"}`)} {
		t.Run(token, func(t *testing.T) {
			var tokenDeadline time.Time
			cli := NewCLI(func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if got := name + " " + strings.Join(args, " "); got != "az account get-access-token --resource https://graph.microsoft.com/ --tenant tenant-1 --output json" {
					t.Fatalf("unexpected token command: %s", got)
				}
				tokenDeadline, _ = ctx.Deadline()
				return json.Marshal(map[string]string{"accessToken": token})
			})
			requests := 0
			client := accountHTTPClient(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.Method != http.MethodGet || req.URL.String() != "https://graph.microsoft.com/v1.0/me?$select=id,displayName,userPrincipalName" {
					t.Fatalf("unexpected live self request: %s %s", req.Method, req.URL)
				}
				if req.Header.Get("Authorization") != "Bearer "+token {
					t.Fatal("Graph must use the tenant-scoped token")
				}
				if deadline, ok := req.Context().Deadline(); !ok || !deadline.Equal(tokenDeadline) {
					t.Fatal("CLI and HTTP must share one probe deadline")
				}
				return accountResponse(200, `{"id":"principal-1","displayName":"Ada Lovelace","userPrincipalName":"ada@example.com"}`), nil
			})

			got, err := cli.checkAccount(WithTenant(context.Background(), "tenant-1"), client)
			want := AccountHealth{TenantID: "tenant-1", PrincipalID: "principal-1", DisplayName: "Ada Lovelace", UserPrincipalName: "ada@example.com"}
			if err != nil || got != want || requests != 1 {
				t.Fatalf("expected live account %#v and one lookup, got %#v, requests=%d, error=%v", want, got, requests, err)
			}
		})
	}
}

func TestCheckAccountRequiresTenantBeforeAcquiringToken(t *testing.T) {
	cli := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("account check without a tenant must not acquire a token")
		return nil, nil
	})
	_, err := cli.CheckAccount(context.Background())
	if err == nil || errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("missing tenant must leave health unverified: %v", err)
	}
}

func TestCheckAccountClassifiesCLIAccountFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		details     string
		unavailable bool
	}{
		{"login required", "Please run 'az login' to setup account.", true},
		{"missing user", "AADSTS50034: The user account does not exist in the directory.", true},
		{"disabled user", "AADSTS50057: User account is disabled.", true},
		{"removed guest", "AADSTS50020: User account does not exist in tenant.", true},
		{"expired refresh", "AADSTS700082: The refresh token has expired due to inactivity.", true},
		{"revoked grant", "AADSTS50173: The provided grant has expired due to it being revoked.", true},
		{"consent missing", "AADSTS65001: The user or administrator has not consented to use the application.", false},
		{"permission login guidance", "AADSTS65001: Consent required. Run az login --scope https://graph.microsoft.com/.default", false},
		{"network", "Unable to resolve login.microsoftonline.com", false},
		{"throttle", "AADSTS90055: Tenant throttling limit exceeded.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commandErr := errors.New("exit status 1")
			cli := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
				return []byte(tc.details), commandErr
			})
			_, err := cli.checkAccount(WithTenant(context.Background(), "tenant-1"), accountHTTPClient(func(*http.Request) (*http.Response, error) {
				t.Fatal("failed token acquisition must not call Graph")
				return nil, nil
			}))
			if err == nil || errors.Is(err, ErrAccountUnavailable) != tc.unavailable || !errors.Is(err, commandErr) || !strings.Contains(err.Error(), tc.details) {
				t.Fatalf("expected unavailable=%t with original CLI details and cause, got %v", tc.unavailable, err)
			}
		})
	}
}

func TestCheckAccountClassifiesGraphFailuresConservatively(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		unavailable bool
	}{
		{"missing self", 404, `{"error":{"code":"Request_ResourceNotFound","message":"Resource does not exist."}}`, true},
		{"invalid sign-in", 401, `{"error":{"code":"InvalidAuthenticationToken","message":"Access token has expired."}}`, true},
		{"CAE interaction required", 401, `{"error":{"code":"InvalidAuthenticationToken","message":"Continuous access evaluation resulted in challenge with result: InteractionRequired and code: TokenCreatedWithOutdatedPolicies"}}`, false},
		{"disabled account", 403, `{"error":{"code":"Forbidden","message":"AADSTS50057: User account is disabled."}}`, true},
		{"restricted Graph", 403, `{"error":{"code":"Authorization_RequestDenied","message":"Insufficient privileges to complete the operation."}}`, false},
		{"no delegated permissions", 401, `{"error":{"code":"NoPermissionsInAccessToken","message":"The token contains no permissions."}}`, false},
		{"service unavailable", 503, `{"error":{"code":"ServiceUnavailable","message":"Service unavailable."}}`, false},
		{"unstructured unauthorized", 401, `upstream failure`, false},
		{"invalid JSON", 200, `not-json`, false},
		{"missing ID", 200, `{"displayName":"Ada"}`, false},
		{"blank ID", 200, `{"id":"  "}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := accountTokenCLI("opaque-graph-token")
			got, err := cli.checkAccount(WithTenant(context.Background(), "tenant-1"), accountHTTPClient(func(*http.Request) (*http.Response, error) {
				return accountResponse(tc.status, tc.body), nil
			}))
			if err == nil || errors.Is(err, ErrAccountUnavailable) != tc.unavailable || got != (AccountHealth{}) {
				t.Fatalf("expected zero account and unavailable=%t, got %#v, %v", tc.unavailable, got, err)
			}
			if strings.Contains(err.Error(), "opaque-graph-token") {
				t.Fatal("probe errors must not expose the access token")
			}
		})
	}
}

func TestCheckAccountLeavesWWWAuthenticateClaimsChallengeUnverified(t *testing.T) {
	cli := accountTokenCLI("opaque-graph-token")
	got, err := cli.checkAccount(WithTenant(context.Background(), "tenant-1"), accountHTTPClient(func(*http.Request) (*http.Response, error) {
		resp := accountResponse(http.StatusUnauthorized, `{"error":{"code":"InvalidAuthenticationToken","message":"CompactToken parsing failed"}}`)
		resp.Header.Set("Www-Authenticate", `Bearer realm="", authorization_uri="https://login.microsoftonline.com/common/oauth2/authorize", error="insufficient_claims", claims="eyJhY2Nlc3NfdG9rZW4iOnt9fQ=="`)
		return resp, nil
	}))
	if err == nil || errors.Is(err, ErrAccountUnavailable) || got != (AccountHealth{}) {
		t.Fatalf("expected WWW-Authenticate claims challenge to stay unverified (not unavailable), got %#v, %v", got, err)
	}
}

func TestCheckAccountFallsBackToLiveARMProbeOnGraphCAEChallenge(t *testing.T) {
	armTok := graphToken(`{"tid":"tenant-1","oid":"principal-1","name":"Ada Lovelace","upn":"ada@gn.group"}`)
	const nextPage = "https://management.azure.com/tenants?api-version=2022-12-01&$skiptoken=second"

	tokenCalls := 0
	armToken := func(context.Context, string, ...string) ([]byte, error) {
		tokenCalls++
		return json.Marshal(map[string]string{"accessToken": armTok})
	}
	var urls []string
	got, err := checkAfterGraphChallenge(t, armToken, func(req *http.Request) (*http.Response, error) {
		urls = append(urls, req.URL.String())
		if req.Header.Get("Authorization") != "Bearer "+armTok {
			t.Fatal("tenant listing must use the token whose identity was checked")
		}
		switch req.URL.String() {
		case tenantListURL:
			return accountResponse(http.StatusOK, `{"value":[{"tenantId":"other"}],"nextLink":"`+nextPage+`"}`), nil
		case nextPage:
			return accountResponse(http.StatusOK, `{"value":[{"tenantId":"TENANT-1"}]}`), nil
		default:
			t.Fatalf("unexpected tenant listing request: %s", req.URL)
			return nil, nil
		}
	})

	if err != nil {
		t.Fatalf("expected CAE fallback to succeed without error, got %v", err)
	}
	want := AccountHealth{
		TenantID:          "tenant-1",
		PrincipalID:       "principal-1",
		DisplayName:       "Ada Lovelace",
		UserPrincipalName: "ada@gn.group",
	}
	if got != want {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
	if tokenCalls != 1 || len(urls) != 2 {
		t.Fatalf("expected one ARM token and both tenant pages, got token calls=%d, requests=%v", tokenCalls, urls)
	}
}

func TestARMFallbackPreservesTokenAcquisitionFailures(t *testing.T) {
	for _, tc := range []struct {
		details     string
		cause       error
		unavailable bool
	}{
		{"AADSTS50057: User account is disabled.", errors.New("exit status 1"), true},
		{"AADSTS65001: Consent required. Run az login", errors.New("exit status 1"), false},
		{"Please run az login", context.DeadlineExceeded, false},
	} {
		t.Run(tc.details, func(t *testing.T) {
			got, err := checkAfterGraphChallenge(t, func(context.Context, string, ...string) ([]byte, error) {
				return []byte(tc.details), tc.cause
			}, func(*http.Request) (*http.Response, error) {
				t.Fatal("must not query tenants without an ARM token")
				return nil, nil
			})
			if got != (AccountHealth{}) || !errors.Is(err, tc.cause) || errors.Is(err, ErrAccountUnavailable) != tc.unavailable {
				t.Fatalf("expected unavailable=%t with ARM acquisition cause, got %#v, %v", tc.unavailable, got, err)
			}
		})
	}
}

func TestARMFallbackRequiresMatchingIdentityClaims(t *testing.T) {
	for _, token := range []string{
		"opaque",
		strings.TrimSuffix(graphToken(`{"tid":"tenant-1","oid":"principal-1"}`), ".signature"),
		graphToken(`{"oid":"principal-1"}`),
		graphToken(`{"tid":"tenant-1","oid":" "}`),
		graphToken(`{"tid":"tenant-2","oid":"principal-1"}`),
		graphToken(`{"tid":"tenant-1","oid":"principal-2"}`),
	} {
		t.Run(token, func(t *testing.T) {
			got, err := checkAfterGraphChallenge(t, accountTokenCLI(token).run, func(*http.Request) (*http.Response, error) {
				t.Fatal("must not use a token with missing or mismatched identity")
				return nil, nil
			})
			if got != (AccountHealth{}) || err == nil || errors.Is(err, ErrAccountUnavailable) {
				t.Fatalf("unusable identity must leave health unverified: %#v, %v", got, err)
			}
		})
	}
}

func TestARMFallbackRequiresVerifiedTenantMembership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		location string
	}{
		{name: "tenant absent", status: 200, body: `{"value":[{"tenantId":"other"}]}`},
		{name: "unreadable list", status: 200, body: `not JSON`},
		{name: "empty list", status: 200, body: `{"value":[]}`},
		{name: "permission denied", status: 403, body: `{"error":{"code":"AuthorizationFailed"}}`},
		{name: "throttled", status: 429, body: `{"error":{"code":"TooManyRequests"}}`},
		{name: "untrusted next link", status: 200, body: `{"value":[],"nextLink":"https://other.management.azure.com/tenants"}`},
		{name: "untrusted redirect", status: 302, location: "https://other.management.azure.com/tenants"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := graphToken(`{"tid":"tenant-1","oid":"principal-1"}`)
			got, err := checkAfterGraphChallenge(t, accountTokenCLI(token).run, func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != tenantListURL {
					t.Fatalf("unexpected authenticated request to %s", req.URL)
				}
				resp := accountResponse(tc.status, tc.body)
				if tc.location != "" {
					resp.Header.Set("Location", tc.location)
				}
				return resp, nil
			})
			if got != (AccountHealth{}) || err == nil || errors.Is(err, ErrAccountUnavailable) {
				t.Fatalf("unverified tenant membership must not prove health or sign-in failure: %#v, %v", got, err)
			}
		})
	}
}

func checkAfterGraphChallenge(t *testing.T, armToken Runner, armHTTP accountRoundTripper) (AccountHealth, error) {
	t.Helper()
	cli := NewCLI(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "account get-access-token --resource https://graph.microsoft.com/ --tenant tenant-1 --output json":
			return json.Marshal(map[string]string{"accessToken": graphToken(`{"tid":"tenant-1","oid":"principal-1"}`)})
		case "account get-access-token --resource " + arm.Resource + " --tenant tenant-1 --output json":
			return armToken(ctx, name, args...)
		default:
			t.Fatalf("unexpected Azure CLI arguments: %v", args)
			return nil, nil
		}
	})
	return cli.checkAccount(WithTenant(context.Background(), "tenant-1"), accountHTTPClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "graph.microsoft.com" {
			return accountResponse(http.StatusUnauthorized, `{"error":{"code":"InvalidAuthenticationToken","message":"Continuous access evaluation resulted in challenge with result: InteractionRequired and code: TokenCreatedWithOutdatedPolicies"}}`), nil
		}
		return armHTTP(req)
	}))
}

func TestCheckAccountRejectsReadableTokenIdentityMismatch(t *testing.T) {
	for _, claims := range []string{
		`{"tid":"tenant-2","oid":"principal-1"}`,
		`{"tid":"tenant-1","oid":"principal-2"}`,
	} {
		t.Run(claims, func(t *testing.T) {
			got, err := accountTokenCLI(graphToken(claims)).checkAccount(WithTenant(context.Background(), "tenant-1"), accountHTTPClient(func(*http.Request) (*http.Response, error) {
				return accountResponse(200, `{"id":"principal-1"}`), nil
			}))
			if err == nil || errors.Is(err, ErrAccountUnavailable) || got != (AccountHealth{}) {
				t.Fatalf("identity mismatch must not report a found or missing account: %#v, %v", got, err)
			}
		})
	}
}

func TestCheckAccountPreservesCLIContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(WithTenant(context.Background(), "tenant-1"))
	cancel()
	cli := NewCLI(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("Please run az login"), ctx.Err()
	})
	_, err := cli.CheckAccount(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("cancellation must remain unverified: %v", err)
	}
}

func TestCheckAccountPreservesHTTPFailuresAndParentDeadline(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled, errors.New("network unreachable")} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(WithTenant(context.Background(), "tenant-1"), time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			_, err := accountTokenCLI("opaque-graph-token").checkAccount(ctx, accountHTTPClient(func(req *http.Request) (*http.Response, error) {
				if got, _ := req.Context().Deadline(); !got.Equal(deadline) {
					t.Fatal("probe must retain the shorter caller deadline")
				}
				return nil, cause
			}))
			if !errors.Is(err, cause) || errors.Is(err, ErrAccountUnavailable) {
				t.Fatalf("HTTP failure must remain unverified and preserve cause: %v", err)
			}
		})
	}
}

func TestCheckAccountIdentifiesFailedStage(t *testing.T) {
	for _, stage := range []string{"Graph token acquisition", "Graph self lookup"} {
		for _, cause := range []error{context.DeadlineExceeded, errors.New("network unavailable")} {
			t.Run(stage+"/"+cause.Error(), func(t *testing.T) {
				cli := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
					if stage == "Graph token acquisition" {
						return nil, cause
					}
					return []byte(`{"accessToken":"opaque-token"}`), nil
				})
				_, err := cli.checkAccount(WithTenant(context.Background(), "tenant-1"), accountHTTPClient(func(*http.Request) (*http.Response, error) {
					return nil, cause
				}))
				if !errors.Is(err, cause) || !strings.Contains(err.Error(), stage) || errors.Is(err, ErrAccountUnavailable) {
					t.Fatalf("expected advisory %s failure preserving %v, got %v", stage, cause, err)
				}
			})
		}
	}
}

func TestAccessTokenPreservesActionableCLIOutput(t *testing.T) {
	cause := errors.New("exit status 1")
	cli := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("AADSTS50057: User account is disabled."), cause
	})
	_, err := cli.AccessToken(context.Background(), "https://graph.microsoft.com/")
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "AADSTS50057") {
		t.Fatalf("token acquisition must retain actionable CLI output: %v", err)
	}
}

func TestAccessTokenClassifiesDefiniteUnusableSignIn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		details     string
		cause       error
		unavailable bool
	}{
		{"disabled", "AADSTS50057: User account is disabled.", errors.New("exit status 1"), true},
		{"revoked", "AADSTS50173: The provided grant has expired due to it being revoked.", errors.New("exit status 1"), true},
		{"login required", "Please run az login", errors.New("exit status 1"), true},
		{"consent required", "AADSTS65001: Consent required. Run az login", errors.New("exit status 1"), false},
		{"canceled", "Please run az login", context.Canceled, false},
		{"timeout", "AADSTS50057: User account is disabled.", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
				return []byte(tc.details), tc.cause
			})
			_, err := cli.AccessToken(WithTenant(context.Background(), "tenant-1"), "https://management.core.windows.net/")
			if err == nil || errors.Is(err, ErrAccountUnavailable) != tc.unavailable || !errors.Is(err, tc.cause) || !strings.Contains(err.Error(), tc.details) {
				t.Fatalf("expected unavailable=%t and preserved CLI cause/details, got %v", tc.unavailable, err)
			}
		})
	}
}

type accountRoundTripper func(*http.Request) (*http.Response, error)

func (f accountRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func accountHTTPClient(transport accountRoundTripper) *http.Client {
	return &http.Client{Transport: transport}
}

func accountResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func accountTokenCLI(token string) CLI {
	return NewCLI(func(context.Context, string, ...string) ([]byte, error) {
		return json.Marshal(map[string]string{"accessToken": token})
	})
}

func graphToken(claims string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
}
