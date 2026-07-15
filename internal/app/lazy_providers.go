package app

import (
	"context"
	"sync"

	"github.com/mathwro/pim-manager/internal/pim"
)

type principalSource interface {
	PrincipalID(context.Context) (string, error)
}

type lazyAssignmentProvider struct {
	discover func(context.Context) ([]pim.EligibleAssignment, error)
	activate func(context.Context, pim.ActivationRequest) (pim.ActivationResult, error)
}

func (p lazyAssignmentProvider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	return p.discover(ctx)
}

func (p lazyAssignmentProvider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	return p.activate(ctx, request)
}

type providerResolver struct {
	mu          sync.Mutex
	initialized bool
	provider    lazyAssignmentProvider
}

func (r *providerResolver) get(init func() (lazyAssignmentProvider, error)) (lazyAssignmentProvider, error) {
	r.mu.Lock()
	if r.initialized {
		provider := r.provider
		r.mu.Unlock()
		return provider, nil
	}
	r.mu.Unlock()

	provider, err := init()
	if err != nil {
		return lazyAssignmentProvider{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.initialized {
		return r.provider, nil
	}
	r.provider = provider
	r.initialized = true
	return provider, nil
}

func newLazyPrincipalProvider(principal principalSource, factory func(string) lazyAssignmentProvider) lazyAssignmentProvider {
	resolver := &providerResolver{}
	resolve := func(ctx context.Context) (lazyAssignmentProvider, error) {
		return resolver.get(func() (lazyAssignmentProvider, error) {
			principalID, err := principal.PrincipalID(ctx)
			if err != nil {
				return lazyAssignmentProvider{}, err
			}
			return factory(principalID), nil
		})
	}

	return lazyAssignmentProvider{
		discover: func(ctx context.Context) ([]pim.EligibleAssignment, error) {
			provider, err := resolve(ctx)
			if err != nil {
				return nil, err
			}
			return provider.Discover(ctx)
		},
		activate: func(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
			provider, err := resolve(ctx)
			if err != nil {
				return pim.ActivationResult{}, err
			}
			return provider.Activate(ctx, request)
		},
	}
}
