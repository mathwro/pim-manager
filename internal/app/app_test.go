package app

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/graph"
)

func TestRunDefersAzureCliLookupUntilTuiInteraction(t *testing.T) {
	oldNewCLI := newCLI
	oldRunProgram := runProgram
	t.Cleanup(func() {
		newCLI = oldNewCLI
		runProgram = oldRunProgram
	})

	var runnerCalls int
	newCLI = func(run azureauth.Runner) azureauth.CLI {
		return azureauth.NewCLI(func(ctx context.Context, name string, args ...string) ([]byte, error) {
			runnerCalls++
			return run(ctx, name, args...)
		})
	}
	runProgram = func(model tea.Model) error {
		if model == nil {
			t.Fatal("expected model to be constructed")
		}
		return nil
	}

	if err := Run(); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runnerCalls != 0 {
		t.Fatalf("expected no Azure CLI calls before TUI interaction, got %d", runnerCalls)
	}
}

func TestRunInitListsTenantsWithoutSectionDiscovery(t *testing.T) {
	oldNewCLI := newCLI
	oldRunProgram := runProgram
	oldCheckLatest := checkLatest
	t.Cleanup(func() {
		newCLI = oldNewCLI
		runProgram = oldRunProgram
		checkLatest = oldCheckLatest
	})

	var commandMu sync.Mutex
	var commands []string
	newCLI = func(run azureauth.Runner) azureauth.CLI {
		return azureauth.NewCLI(func(_ context.Context, name string, args ...string) ([]byte, error) {
			command := name
			for _, arg := range args {
				command += " " + arg
			}
			commandMu.Lock()
			commands = append(commands, command)
			commandMu.Unlock()
			switch command {
			case "az rest --method get --url https://management.azure.com/tenants?api-version=2022-12-01 --query value[].{tenantId:tenantId,displayName:displayName,defaultDomain:defaultDomain} --output json":
				return []byte(`[{"tenantId":"tenant-1"}]`), nil
			case "az account list --all --query [].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain} --output json":
				return []byte(`[{"tenantId":"tenant-1","displayName":"Contoso"}]`), nil
			default:
				return nil, errors.New("unexpected command: " + command)
			}
		})
	}
	var checkCalls int
	checkLatest = func(context.Context) (string, error) {
		checkCalls++
		return "", nil
	}
	runProgram = func(model tea.Model) error {
		cmd := model.Init()
		if cmd == nil {
			t.Fatal("expected Init command")
		}
		batch, ok := cmd().(tea.BatchMsg)
		if !ok {
			t.Fatal("expected Init command batch")
		}
		for _, child := range batch {
			_ = child()
		}
		return nil
	}

	if err := Run(); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	commandMu.Lock()
	got := slices.Clone(commands)
	commandMu.Unlock()
	want := []string{
		"az rest --method get --url https://management.azure.com/tenants?api-version=2022-12-01 --query value[].{tenantId:tenantId,displayName:displayName,defaultDomain:defaultDomain} --output json",
		"az account list --all --query [].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain} --output json",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected tenant discovery and name enrichment calls, got %#v", got)
	}
	if checkCalls != 1 {
		t.Fatalf("expected one background update check, got %d", checkCalls)
	}
}

func TestRunConfiguresBoundedHTTPClientTimeout(t *testing.T) {
	oldNewCLI := newCLI
	oldNewARMClient := newARMClient
	oldNewGraphClient := newGraphClient
	oldRunProgram := runProgram
	t.Cleanup(func() {
		newCLI = oldNewCLI
		newARMClient = oldNewARMClient
		newGraphClient = oldNewGraphClient
		runProgram = oldRunProgram
	})

	newCLI = func(azureauth.Runner) azureauth.CLI {
		return azureauth.NewCLI(nil)
	}
	var armHTTPClient, graphHTTPClient *http.Client
	newARMClient = func(client *http.Client, tokenSource arm.TokenSource) arm.Client {
		armHTTPClient = client
		return oldNewARMClient(client, tokenSource)
	}
	newGraphClient = func(client *http.Client, tokenSource graph.TokenSource) graph.Client {
		graphHTTPClient = client
		return oldNewGraphClient(client, tokenSource)
	}
	runProgram = func(tea.Model) error { return nil }

	if err := Run(); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if armHTTPClient == nil || armHTTPClient == http.DefaultClient || armHTTPClient.Timeout != 60*time.Second {
		t.Fatalf("expected ARM HTTP client with 60s timeout, got %#v", armHTTPClient)
	}
	if graphHTTPClient == nil || graphHTTPClient == http.DefaultClient || graphHTTPClient.Timeout != 60*time.Second {
		t.Fatalf("expected Graph HTTP client with 60s timeout, got %#v", graphHTTPClient)
	}
}
