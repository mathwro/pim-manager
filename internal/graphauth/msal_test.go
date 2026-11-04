package graphauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

type discoveryTransport struct{ t *testing.T }

func (d discoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet || request.URL.Host != "login.microsoftonline.com" || !strings.HasSuffix(request.URL.Path, "/.well-known/openid-configuration") {
		d.t.Fatalf("unexpected MSAL network request: %s %s", request.Method, request.URL)
	}
	body := `{"authorization_endpoint":"https://login.microsoftonline.com/11111111-1111-1111-1111-111111111111/oauth2/v2.0/authorize","token_endpoint":"https://login.microsoftonline.com/11111111-1111-1111-1111-111111111111/oauth2/v2.0/token","issuer":"https://login.microsoftonline.com/11111111-1111-1111-1111-111111111111/v2.0"}`
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

// Exercise the real MSAL PKCE URL, stopping at browser launch without any login.
func TestMSALAuthorizeURLUsesHintWithoutForcingAccountChooser(t *testing.T) {
	for _, hint := range []string{"checked-user@example.test", ""} {
		t.Run(map[bool]string{true: "known user", false: "no known user"}[hint != ""], func(t *testing.T) {
			client, err := public.New(microsoftGraphClientID,
				public.WithAuthority("https://login.microsoftonline.com/"+testTenant),
				public.WithInstanceDiscovery(false), public.WithHTTPClient(&http.Client{Transport: discoveryTransport{t: t}}))
			if err != nil {
				t.Fatal(err)
			}
			stopped := errors.New("browser URL captured")
			called := false
			adapter := msalClient{client: client, openURL: func(_ context.Context, address string) error {
				called = true
				u, err := url.Parse(address)
				if err != nil {
					t.Fatal(err)
				}
				if u.Scheme != "https" || u.Host != "login.microsoftonline.com" || u.Path != "/"+testTenant+"/oauth2/v2.0/authorize" {
					t.Fatalf("authorization endpoint changed: %s", address)
				}
				query := u.Query()
				if query.Get("login_hint") != hint {
					t.Fatalf("unexpected login hint: %q", query.Get("login_hint"))
				}
				if hint != "" && query.Has("prompt") {
					t.Fatalf("known-user request forces prompt: %q", query.Get("prompt"))
				}
				if hint == "" && query.Get("prompt") != "select_account" {
					t.Fatalf("unknown user lost account chooser: %q", query.Get("prompt"))
				}
				if query.Get("client_id") != microsoftGraphClientID || query.Get("response_type") != "code" || query.Get("response_mode") != "form_post" {
					t.Fatal("MSAL authorization-code flow parameters changed")
				}
				if query.Get("state") == "" || len(query.Get("code_challenge")) != 43 || query.Get("code_challenge_method") != "S256" {
					t.Fatal("MSAL state or PKCE parameters were lost")
				}
				redirect, err := url.Parse(query.Get("redirect_uri"))
				if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "localhost" || redirect.Port() == "" {
					t.Fatalf("MSAL loopback redirect changed: %s", query.Get("redirect_uri"))
				}
				if query.Get("claims") != `{"access_token":{"acrs":{"essential":true,"value":"c1"}}}` {
					t.Fatalf("authentication claims lost: %s", query.Get("claims"))
				}
				for _, scope := range testScopes {
					if !contains(strings.Fields(query.Get("scope")), scope) {
						t.Fatalf("consent scope %q was lost", scope)
					}
				}
				return stopped
			}}
			_, err = adapter.acquireInteractive(context.Background(), testScopes, `{"access_token":{"acrs":{"essential":true,"value":"c1"}}}`, hint)
			if !called || !errors.Is(err, stopped) {
				t.Fatalf("browser boundary not reached: %v", err)
			}
		})
	}
}

func TestRemovingDefaultChooserPreservesAllOtherAuthorizeFields(t *testing.T) {
	address := "https://login.microsoftonline.com/tenant/oauth2/v2.0/authorize?login_hint=checked%40example.test&prompt=select_account&state=state-value&code_challenge=pkce-value&code_challenge_method=S256&redirect_uri=http%3A%2F%2Flocalhost%3A1234&claims=%7B%22access_token%22%3A%7B%7D%7D&scope=scope-one&scope=scope-two#fragment"
	got, err := browserAuthorizationURL(address, "checked@example.test")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := url.Parse(address)
	after, _ := url.Parse(got)
	wantQuery := before.Query()
	wantQuery.Del("prompt")
	if !reflect.DeepEqual(after.Query(), wantQuery) || after.Scheme != before.Scheme || after.Host != before.Host || after.Path != before.Path || after.Fragment != before.Fragment {
		t.Fatal("removing account chooser changed another authorization parameter")
	}
	for _, tc := range []struct{ address, hint string }{
		{address, ""},
		{address, "different@example.test"},
		{strings.Replace(address, "prompt=select_account", "prompt=login", 1), "checked@example.test"},
		{strings.Replace(address, "login_hint=checked%40example.test&", "", 1), "checked@example.test"},
	} {
		got, err := browserAuthorizationURL(tc.address, tc.hint)
		if err != nil || got != tc.address {
			t.Fatalf("unrelated prompt/hint was changed: %q %v", got, err)
		}
	}
}

// Helper launcher for TestBrowserLaunchDoesNotWaitForBrowserExit.
func TestBrowserLauncherHelper(t *testing.T) {
	switch os.Getenv("GRAPHAUTH_BROWSER_LAUNCHER") {
	case "stay-open":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "fail":
		os.Exit(3)
	}
}

// MSAL processes the sign-in callback only after the opener returns; xdg-open may live as long as the browser.
func TestBrowserLaunchDoesNotWaitForBrowserExit(t *testing.T) {
	args := []string{"-test.run=^TestBrowserLauncherHelper$"}
	t.Setenv("GRAPHAUTH_BROWSER_LAUNCHER", "stay-open")
	start := time.Now()
	if err := launchBrowser(context.Background(), os.Args[0], args, 200*time.Millisecond); err != nil {
		t.Fatalf("running launcher reported failure: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("waited %s for the browser to exit", elapsed)
	}
	t.Setenv("GRAPHAUTH_BROWSER_LAUNCHER", "fail")
	var launchErr *browserError
	if err := launchBrowser(context.Background(), os.Args[0], args, 10*time.Second); !errors.As(err, &launchErr) {
		t.Fatalf("failed launcher was not reported: %v", err)
	}
}
