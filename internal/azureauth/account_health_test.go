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
		{"disabled account", 403, `{"error":{"code":"Forbidden","message":"AADSTS50057: User account is disabled."}}`, true},
		{"restricted Graph", 403, `{"error":{"code":"Authorization_RequestDenied","message":"Insufficient privileges to complete the operation."}}`, false},
		{"no delegated permissions", 401, `{"error":{"code":"NoPermissionsInAccessToken","message":"The token contains no permissions."}}`, false},
		{"throttle", 429, `{"error":{"code":"TooManyRequests","message":"Please retry later."}}`, false},
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
