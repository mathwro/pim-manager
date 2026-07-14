package azureresources

import (
	"context"
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
	if len(scopes) != len(want) {
		t.Fatalf("expected %d scopes, got %#v", len(want), scopes)
	}
	for i := range want {
		if scopes[i] != want[i] {
			t.Fatalf("scope %d: expected %q, got %q", i, want[i], scopes[i])
		}
	}
}

type fakeARM struct {
	responses map[string]any
}

func (f *fakeARM) Get(_ context.Context, path string, out any) error {
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
