package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

type fakePrincipalSource struct {
	id    string
	calls int
	err   error
}

func (f *fakePrincipalSource) PrincipalID(context.Context) (string, error) {
	f.calls++
	return f.id, f.err
}

func TestLazyPrincipalProviderDefersPrincipalUntilDiscover(t *testing.T) {
	principal := &fakePrincipalSource{id: "principal-2"}
	var factoryCalls int
	provider := newLazyPrincipalProvider(principal, func(principalID string) lazyAssignmentProvider {
		factoryCalls++
		if principalID != "principal-2" {
			t.Fatalf("expected principal-2, got %q", principalID)
		}
		return lazyAssignmentProvider{
			discover: func(context.Context) ([]pim.EligibleAssignment, error) {
				return []pim.EligibleAssignment{{ID: "assignment-2"}}, nil
			},
			activate: func(context.Context, pim.ActivationRequest) (pim.ActivationResult, error) {
				return pim.ActivationResult{}, nil
			},
		}
	})

	if principal.calls != 0 {
		t.Fatal("expected constructor not to call principal lookup")
	}

	assignments, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(assignments) != 1 || assignments[0].ID != "assignment-2" {
		t.Fatalf("unexpected assignments: %#v", assignments)
	}
	if principal.calls != 1 || factoryCalls != 1 {
		t.Fatalf("expected one principal lookup and one factory call, got %d and %d", principal.calls, factoryCalls)
	}

	if _, err := provider.Activate(context.Background(), pim.ActivationRequest{}); err != nil {
		t.Fatalf("Activate returned error: %v", err)
	}
	if principal.calls != 1 || factoryCalls != 1 {
		t.Fatalf("expected cached provider to avoid repeated lookups, got %d and %d", principal.calls, factoryCalls)
	}
}

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
