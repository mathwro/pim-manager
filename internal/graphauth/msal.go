package graphauth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

// microsoftGraphClientID is the hardcoded application (client) ID of Microsoft's
// Microsoft Graph Command Line Tools public client, not a tenant-specific service principal object ID.
const microsoftGraphClientID = "14d82eec-204b-4c2f-b7e8-296a70dab67e"

type msalClient struct {
	client  public.Client
	openURL func(context.Context, string) error
}

func newMSALClient(tenant string) (tokenClient, error) {
	// No WithCache accessor: MSAL's token/account cache remains in this session's memory.
	client, err := public.New(microsoftGraphClientID, public.WithAuthority("https://login.microsoftonline.com/"+tenant))
	if err != nil {
		return nil, err
	}
	return msalClient{client: client, openURL: openBrowser}, nil
}

func (c msalClient) acquireInteractive(ctx context.Context, scopes []string, claims, hint string) (public.AuthResult, error) {
	options := []public.AcquireInteractiveOption{public.WithOpenURL(func(address string) error {
		address, err := browserAuthorizationURL(address, hint)
		if err != nil {
			return err
		}
		return c.openURL(ctx, address)
	})}
	if claims != "" {
		options = append(options, public.WithClaims(claims))
	}
	if hint != "" {
		options = append(options, public.WithLoginHint(hint))
	}
	// MSAL owns the loopback listener, state, authorization code exchange, and S256 PKCE.
	return c.client.AcquireTokenInteractive(ctx, scopes, options...)
}

func browserAuthorizationURL(address, hint string) (string, error) {
	if hint == "" {
		return address, nil
	}
	u, err := url.Parse(address)
	if err != nil {
		return "", errors.New("cannot inspect MSAL browser authorization URL")
	}
	query := u.Query()
	if query.Get("login_hint") != hint {
		return address, nil
	}
	prompt := query["prompt"]
	if len(prompt) != 1 || prompt[0] != "select_account" {
		return address, nil
	}
	// MSAL v1.10.1 has no omit-prompt option for interactive acquisition. Its
	// browser callback can omit only the chooser while retaining MFA/consent.
	query.Del("prompt")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (c msalClient) acquireSilent(ctx context.Context, scopes []string, account public.Account, claims string) (public.AuthResult, error) {
	options := []public.AcquireSilentOption{public.WithSilentAccount(account)}
	if claims != "" {
		options = append(options, public.WithClaims(claims))
	}
	return c.client.AcquireTokenSilent(ctx, scopes, options...)
}

func openBrowser(ctx context.Context, url string) error {
	wsl := os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != ""
	if runtime.GOOS == "linux" && !wsl {
		if release, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			wsl = strings.Contains(strings.ToLower(string(release)), "microsoft")
		}
	}
	name, args := browserCommand(runtime.GOOS, wsl, url)
	return launchBrowser(ctx, name, args, browserLaunchGrace)
}

// browserLaunchGrace bounds the wait for launcher failures. Some xdg-open setups
// stay alive until the browser exits, and MSAL handles the callback only after
// the opener returns.
const browserLaunchGrace = 2 * time.Second

// launchBrowser reports launchers that fail within grace, then returns while a
// still-running launcher is reaped in the background. Without CommandContext,
// finishing or canceling sign-in cannot kill a browser the launcher became.
func launchBrowser(ctx context.Context, name string, args []string, grace time.Duration) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return &browserError{executable: name, missing: errors.Is(err, exec.ErrNotFound)}
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-exited:
		if err != nil {
			return &browserError{executable: name}
		}
		return nil
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type browserError struct {
	executable string
	missing    bool
}

func (e *browserError) Error() string {
	if e.missing {
		return fmt.Sprintf("cannot launch browser: install or expose %s on PATH", e.executable)
	}
	return fmt.Sprintf("cannot launch browser using %s; check the default browser and try Connect Microsoft Graph again", e.executable)
}

func browserCommand(platform string, wsl bool, url string) (string, []string) {
	switch platform {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		return "open", []string{url}
	default:
		if wsl {
			return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", "Start-Process -FilePath '" + strings.ReplaceAll(url, "'", "''") + "'"}
		}
		return "xdg-open", []string{url}
	}
}
