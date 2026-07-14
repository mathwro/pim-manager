package app

import (
	"context"
	"sync"

	"github.com/mathwro/pim-manager/internal/pim"
)

type principalSource interface {
	PrincipalID(context.Context) (string, error)
}

type scopeDiscoverer interface {
	Discover(context.Context) ([]string, error)
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
	once     sync.Once
	provider lazyAssignmentProvider
	err      error
}

func (r *providerResolver) get(init func() (lazyAssignmentProvider, error)) (lazyAssignmentProvider, error) {
	r.once.Do(func() {
		r.provider, r.err = init()
	})
	return r.provider, r.err
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

func newLazyAzureResourcesProvider(principal principalSource, scopes scopeDiscoverer, factory func(string, []string) lazyAssignmentProvider) lazyAssignmentProvider {
	resolver := &providerResolver{}
	resolve := func(ctx context.Context) (lazyAssignmentProvider, error) {
		return resolver.get(func() (lazyAssignmentProvider, error) {
			principalID, err := principal.PrincipalID(ctx)
			if err != nil {
				return lazyAssignmentProvider{}, err
			}
			scopeValues, err := scopes.Discover(ctx)
			if err != nil {
				return lazyAssignmentProvider{}, err
			}
			return factory(principalID, scopeValues), nil
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
