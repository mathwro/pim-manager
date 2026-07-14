package activation

import (
	"context"

	"github.com/mathwro/pim-manager/internal/pim"
)

type Provider interface {
	Activate(context.Context, pim.ActivationRequest) (pim.ActivationResult, error)
}

type Service struct {
	provider Provider
}

func NewService(provider Provider) Service {
	return Service{provider: provider}
}

func (s Service) ActivateBatch(ctx context.Context, requests []pim.ActivationRequest) []pim.ActivationResult {
	results := make([]pim.ActivationResult, 0, len(requests))
	for _, request := range requests {
		result, err := s.provider.Activate(ctx, request)
		if err != nil {
			result = pim.ActivationResult{
				Assignment: request.Assignment,
				Status:     pim.ActivationStatusFailed,
				Message:    err.Error(),
				Retryable:  IsRetryable(err),
			}
		}
		results = append(results, result)
	}
	return results
}
