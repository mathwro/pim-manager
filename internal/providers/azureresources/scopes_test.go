package azureresources

import (
	"context"
	"errors"
	"testing"
)

func TestScopeDiscovererReturnsManagementGroupsSubscriptionsAndResourceGroups(t *testing.T) {
	arm := &fakeARM{
		responses: map[string]any{
			"/providers/Microsoft.Management/managementGroups?api-version=2020-05-01": managementGroupsResponse{Value: []managementGroup{{Name: "mg-root"}}},
			"/subscriptions?api-version=2020-01-01":                                   subscriptionsResponse{Value: []subscription{{SubscriptionID: "sub-1"}}},
			"/subscriptions/sub-1/resourcegroups?api-version=2021-04-01":              resourceGroupsResponse{Value: []resourceGroup{{ID: "/subscriptions/sub-1/resourceGroups/rg-prod"}}},
		},
	}
	discoverer := NewScopeDiscoverer(arm)

	scopes, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	want := []string{
		"/providers/Microsoft.Management/managementGroups/mg-root",
		"/subscriptions/sub-1",
		"/subscriptions/sub-1/resourceGroups/rg-prod",
	}
	assertScopes(t, scopes, want)
}

func TestScopeDiscovererFollowsPaginatedManagementGroups(t *testing.T) {
	arm := &fakeARM{
		responses: map[string]any{
			"/providers/Microsoft.Management/managementGroups?api-version=2020-05-01": managementGroupsResponse{
				Value:    []managementGroup{{Name: "mg-root"}},
				NextLink: "https://management.azure.com/providers/Microsoft.Management/managementGroups?$skiptoken=next",
			},
			"https://management.azure.com/providers/Microsoft.Management/managementGroups?$skiptoken=next": managementGroupsResponse{Value: []managementGroup{{Name: "mg-child"}}},
			"/subscriptions?api-version=2020-01-01":                                                        subscriptionsResponse{},
		},
	}
	discoverer := NewScopeDiscoverer(arm)

	scopes, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	want := []string{
		"/providers/Microsoft.Management/managementGroups/mg-root",
		"/providers/Microsoft.Management/managementGroups/mg-child",
	}
	assertScopes(t, scopes, want)
}

func TestScopeDiscovererFollowsPaginatedSubscriptions(t *testing.T) {
	arm := &fakeARM{
		responses: map[string]any{
			"/providers/Microsoft.Management/managementGroups?api-version=2020-05-01": managementGroupsResponse{},
			"/subscriptions?api-version=2020-01-01": subscriptionsResponse{
				Value:    []subscription{{SubscriptionID: "sub-1"}},
				NextLink: "https://management.azure.com/subscriptions?$skiptoken=next",
			},
			"https://management.azure.com/subscriptions?$skiptoken=next": subscriptionsResponse{Value: []subscription{{SubscriptionID: "sub-2"}}},
			"/subscriptions/sub-1/resourcegroups?api-version=2021-04-01": resourceGroupsResponse{},
			"/subscriptions/sub-2/resourcegroups?api-version=2021-04-01": resourceGroupsResponse{},
		},
	}
	discoverer := NewScopeDiscoverer(arm)

	scopes, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	want := []string{
		"/subscriptions/sub-1",
		"/subscriptions/sub-2",
	}
	assertScopes(t, scopes, want)
}

func TestScopeDiscovererFollowsPaginatedResourceGroups(t *testing.T) {
	arm := &fakeARM{
		responses: map[string]any{
			"/providers/Microsoft.Management/managementGroups?api-version=2020-05-01": managementGroupsResponse{},
			"/subscriptions?api-version=2020-01-01":                                   subscriptionsResponse{Value: []subscription{{SubscriptionID: "sub-1"}}},
			"/subscriptions/sub-1/resourcegroups?api-version=2021-04-01": resourceGroupsResponse{
				Value:    []resourceGroup{{ID: "/subscriptions/sub-1/resourceGroups/rg-prod"}},
				NextLink: "https://management.azure.com/subscriptions/sub-1/resourcegroups?$skiptoken=next",
			},
			"https://management.azure.com/subscriptions/sub-1/resourcegroups?$skiptoken=next": resourceGroupsResponse{Value: []resourceGroup{{ID: "/subscriptions/sub-1/resourceGroups/rg-dev"}}},
		},
	}
	discoverer := NewScopeDiscoverer(arm)

	scopes, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	want := []string{
		"/subscriptions/sub-1",
		"/subscriptions/sub-1/resourceGroups/rg-prod",
		"/subscriptions/sub-1/resourceGroups/rg-dev",
	}
	assertScopes(t, scopes, want)
}

func TestScopeDiscovererPreservesManagementGroupErrors(t *testing.T) {
	wantErr := errors.New("management groups failed")
	arm := &fakeARM{errors: map[string]error{
		"/providers/Microsoft.Management/managementGroups?api-version=2020-05-01": wantErr,
	}}
	discoverer := NewScopeDiscoverer(arm)

	_, err := discoverer.Discover(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestScopeDiscovererPreservesSubscriptionErrors(t *testing.T) {
	wantErr := errors.New("subscriptions failed")
	arm := &fakeARM{
		responses: map[string]any{
			"/providers/Microsoft.Management/managementGroups?api-version=2020-05-01": managementGroupsResponse{},
		},
		errors: map[string]error{
			"/subscriptions?api-version=2020-01-01": wantErr,
		},
	}
	discoverer := NewScopeDiscoverer(arm)

	_, err := discoverer.Discover(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestScopeDiscovererPreservesResourceGroupErrors(t *testing.T) {
	wantErr := errors.New("resource groups failed")
	arm := &fakeARM{
		responses: map[string]any{
			"/providers/Microsoft.Management/managementGroups?api-version=2020-05-01": managementGroupsResponse{},
			"/subscriptions?api-version=2020-01-01":                                   subscriptionsResponse{Value: []subscription{{SubscriptionID: "sub-1"}}},
		},
		errors: map[string]error{
			"/subscriptions/sub-1/resourcegroups?api-version=2021-04-01": wantErr,
		},
	}
	discoverer := NewScopeDiscoverer(arm)

	_, err := discoverer.Discover(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func assertScopes(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d scopes, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scope %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

type fakeARM struct {
	responses map[string]any
	errors    map[string]error
}

func (f *fakeARM) Get(_ context.Context, path string, out any) error {
	if err := f.errors[path]; err != nil {
		return err
	}
	switch target := out.(type) {
	case *managementGroupsResponse:
		*target = f.responses[path].(managementGroupsResponse)
	case *subscriptionsResponse:
		*target = f.responses[path].(subscriptionsResponse)
	case *resourceGroupsResponse:
		*target = f.responses[path].(resourceGroupsResponse)
	}
	return nil
}

func (f *fakeARM) Put(context.Context, string, any, any) error {
	return nil
}
