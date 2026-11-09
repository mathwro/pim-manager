package azureauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The launcher forwards through a real shell to this process, like az.cmd forwards to Python.
func installAzureCLILauncher(t *testing.T) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Azure CLI with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "az"
	launcher := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestAzureCLILauncherChild$ -- \"$@\"\n"
	if runtime.GOOS == "windows" {
		name += ".cmd"
		launcher = "@echo off\r\n\"" + executable + "\" -test.run=^TestAzureCLILauncherChild$ -- %*\r\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PIM_TEST_AZ_CHILD", "1")
}

func TestAzureCLILauncherChild(t *testing.T) {
	if os.Getenv("PIM_TEST_AZ_CHILD") != "1" {
		return
	}
	separator := 0
	for separator < len(os.Args) && os.Args[separator] != "--" {
		separator++
	}
	args := os.Args[separator+1:]
	if os.Getenv("PIM_TEST_AZ_ERROR") == "1" {
		_, _ = os.Stderr.WriteString("controlled CLI failure")
		os.Exit(3)
	}
	if marker := os.Getenv("PIM_TEST_AZ_WAIT"); marker != "" {
		if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	switch {
	case len(args) > 0 && args[0] == "rest":
		_, _ = os.Stdout.WriteString(`[{"tenantId":"tenant-1","displayName":"Contoso"}]`)
	case len(args) > 1 && args[0] == "account" && args[1] == "list":
		_, _ = os.Stdout.WriteString(`[]`)
	case len(args) > 3 && args[0] == "account" && args[1] == "get-access-token":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"accessToken": args[3]})
	default:
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Args            []string
			LoginExperience string
		}{args, os.Getenv("AZURE_CORE_LOGIN_EXPERIENCE_V2")})
	}
	os.Exit(0)
}

func TestAzureCLILauncherDiscoversTenants(t *testing.T) {
	installAzureCLILauncher(t)
	tenants, err := NewCLI(nil).Tenants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []Tenant{{ID: "tenant-1", DisplayName: "Contoso"}}; !reflect.DeepEqual(tenants, want) {
		t.Fatalf("tenants = %#v, want %#v", tenants, want)
	}
}

func TestAzureCLILauncherPreservesTokenArguments(t *testing.T) {
	installAzureCLILauncher(t)
	t.Setenv("PIM_TEST_VALUE", "expanded value")
	for _, resource := range []string{
		"https://management.core.windows.net/",
		"spaces and\ttabs",
		`%PIM_TEST_VALUE% !PIM_TEST_VALUE! ^ & | < > ( )`,
		`trailing slash\`,
		"日本語",
	} {
		t.Run(resource, func(t *testing.T) {
			token, err := NewCLI(nil).AccessToken(context.Background(), resource)
			if err != nil || token != resource {
				t.Fatalf("token = %q, error = %v; want literal %q", token, err, resource)
			}
		})
	}
}

func TestAzureCLILauncherStepUpClaimsAndChildEnvironment(t *testing.T) {
	installAzureCLILauncher(t)
	t.Setenv("AZURE_CORE_LOGIN_EXPERIENCE_V2", "on")
	for _, tc := range []struct {
		name   string
		mfa    bool
		acrs   string
		claims string
	}{
		{"mfa", true, "", `{"access_token":{"amr":{"essential":true,"values":["mfa"]}}}`},
		{"context", false, " c1 ", `{"access_token":{"acrs":{"essential":true,"value":"c1"}}}`},
		{"combined", true, "c1", `{"access_token":{"acrs":{"essential":true,"value":"c1"},"amr":{"essential":true,"values":["mfa"]}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, err := StepUpLoginCommand(" tenant-1 ", tc.mfa, tc.acrs)
			if err != nil {
				t.Fatal(err)
			}
			out, err := command.Output()
			if err != nil {
				t.Fatal(azureCLIError(out, err))
			}
			var child struct {
				Args            []string
				LoginExperience string
			}
			if err := json.Unmarshal(out, &child); err != nil {
				t.Fatalf("decode child output %q: %v", out, err)
			}
			want := []string{"login", "--tenant", "tenant-1", "--scope", "https://management.core.windows.net//.default", "--claims-challenge", base64.StdEncoding.EncodeToString([]byte(tc.claims)), "--output", "none"}
			if !reflect.DeepEqual(child.Args, want) || child.LoginExperience != "off" {
				t.Fatalf("child = %#v, want args %#v with selector off", child, want)
			}
			if got := os.Getenv("AZURE_CORE_LOGIN_EXPERIENCE_V2"); got != "on" {
				t.Fatalf("parent login experience changed to %q", got)
			}
		})
	}
}

func TestAzureCLILauncherPreservesExitDetails(t *testing.T) {
	installAzureCLILauncher(t)
	t.Setenv("PIM_TEST_AZ_ERROR", "1")
	_, err := NewCLI(nil).AccessToken(context.Background(), "resource")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 || !strings.Contains(err.Error(), "controlled CLI failure") {
		t.Fatalf("expected wrapped exit status and stderr, got %v", err)
	}
}
