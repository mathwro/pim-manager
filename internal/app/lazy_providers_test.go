package app

import (
	"context"
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

type fakeScopeDiscoverer struct {
	scopes []string
	calls  int
	err    error
}

func (f *fakeScopeDiscoverer) Discover(context.Context) ([]string, error) {
	f.calls++
	return f.scopes, f.err
}

type fakeProviderFactory struct {
	principalID string
	scopes      []string
	discovered  []pim.EligibleAssignment
}

func (f *fakeProviderFactory) newProvider(principalID string, scopes []string) lazyAssignmentProvider {
	f.principalID = principalID
	f.scopes = scopes
	return lazyAssignmentProvider{
		discover: func(context.Context) ([]pim.EligibleAssignment, error) {
			return f.discovered, nil
		},
		activate: func(context.Context, pim.ActivationRequest) (pim.ActivationResult, error) {
			return pim.ActivationResult{}, nil
		},
	}
}

func TestLazyAzureResourcesProviderDefersPrincipalAndScopesUntilDiscover(t *testing.T) {
	principal := &fakePrincipalSource{id: "principal-1"}
	scopes := &fakeScopeDiscoverer{scopes: []string{"/subscriptions/sub-1"}}
	factory := &fakeProviderFactory{discovered: []pim.EligibleAssignment{{ID: "assignment-1"}}}
	provider := newLazyAzureResourcesProvider(principal, scopes, factory.newProvider)

	if principal.calls != 0 || scopes.calls != 0 {
		t.Fatal("expected constructor not to call Azure dependencies")
	}

	assignments, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(assignments) != 1 || assignments[0].ID != "assignment-1" {
		t.Fatalf("unexpected assignments: %#v", assignments)
	}
	if factory.principalID != "principal-1" || len(factory.scopes) != 1 {
		t.Fatalf("expected principal and scopes passed to factory, got %q %#v", factory.principalID, factory.scopes)
	}
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
