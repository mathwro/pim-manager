package activation

import (
	"context"
	"errors"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestActivateBatchContinuesAfterFailure(t *testing.T) {
	provider := &fakeProvider{
		results: map[string]pim.ActivationResult{
			"two": {Status: pim.ActivationStatusActivated},
		},
		errors: map[string]error{
			"one": errors.New("throttled"),
		},
	}
	service := NewService(provider)

	results := service.ActivateBatch(context.Background(), []pim.ActivationRequest{
		{Assignment: pim.EligibleAssignment{ID: "one"}},
		{Assignment: pim.EligibleAssignment{ID: "two"}},
	})

	if len(results) != 2 {
		t.Fatalf("expected two results, got %d", len(results))
	}
	if !results[0].CanRetry() {
		t.Fatalf("expected first result retryable failure: %#v", results[0])
	}
	if results[0].Message != "throttled" {
		t.Fatalf("expected first result message %q, got %q", "throttled", results[0].Message)
	}
	if !results[1].Success() {
		t.Fatalf("expected second result success: %#v", results[1])
	}
	if len(provider.calls) != 2 || provider.calls[0] != "one" || provider.calls[1] != "two" {
		t.Fatalf("expected calls [one two], got %#v", provider.calls)
	}
}

func TestActivateBatchMapsProviderErrorToFailedResult(t *testing.T) {
	service := NewService(&fakeProvider{err: errors.New("network down")})
	assignment := pim.EligibleAssignment{
		ID:          "one",
		Source:      pim.AssignmentSourceEntra,
		Kind:        pim.AssignmentKindDirectoryRole,
		DisplayName: "Privileged Role Administrator",
		Scope: pim.Scope{
			ID:          "/",
			DisplayName: "Tenant Root",
			Type:        pim.ScopeTypeTenant,
		},
	}

	results := service.ActivateBatch(context.Background(), []pim.ActivationRequest{
		{Assignment: assignment},
	})

	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].Status != pim.ActivationStatusFailed || !results[0].Retryable {
		t.Fatalf("expected retryable failure, got %#v", results[0])
	}
	if results[0].Message != "network down" {
		t.Fatalf("expected error message %q, got %q", "network down", results[0].Message)
	}
	if results[0].Assignment != assignment {
		t.Fatalf("expected assignment %#v, got %#v", assignment, results[0].Assignment)
	}
}

type fakeProvider struct {
	results map[string]pim.ActivationResult
	errors  map[string]error
	err     error
	calls   []string
}

func (f *fakeProvider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	f.calls = append(f.calls, request.Assignment.ID)
	if f.err != nil {
		return pim.ActivationResult{}, f.err
	}
	if err := f.errors[request.Assignment.ID]; err != nil {
		return pim.ActivationResult{}, err
	}
	result := f.results[request.Assignment.ID]
	result.Assignment = request.Assignment
	return result, nil
}
