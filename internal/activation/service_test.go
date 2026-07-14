package activation

import (
	"context"
	"errors"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestActivateBatchContinuesAfterFailure(t *testing.T) {
	provider := fakeProvider{
		results: map[string]pim.ActivationResult{
			"one": {Status: pim.ActivationStatusActivated},
			"two": {Status: pim.ActivationStatusFailed, Message: "throttled", Retryable: true},
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
	if !results[0].Success() {
		t.Fatalf("expected first result success: %#v", results[0])
	}
	if !results[1].CanRetry() {
		t.Fatalf("expected second result retryable failure: %#v", results[1])
	}
}

func TestActivateBatchMapsProviderErrorToFailedResult(t *testing.T) {
	service := NewService(fakeProvider{err: errors.New("network down")})

	results := service.ActivateBatch(context.Background(), []pim.ActivationRequest{
		{Assignment: pim.EligibleAssignment{ID: "one"}},
	})

	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].Status != pim.ActivationStatusFailed || !results[0].Retryable {
		t.Fatalf("expected retryable failure, got %#v", results[0])
	}
}

type fakeProvider struct {
	results map[string]pim.ActivationResult
	err     error
}

func (f fakeProvider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	if f.err != nil {
		return pim.ActivationResult{}, f.err
	}
	result := f.results[request.Assignment.ID]
	result.Assignment = request.Assignment
	return result, nil
}
