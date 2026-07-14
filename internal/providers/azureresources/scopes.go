package azureresources

import (
	"context"
	"fmt"
)

type ARMClient interface {
	Get(context.Context, string, any) error
	Put(context.Context, string, any, any) error
}

type ScopeDiscoverer struct {
	arm ARMClient
}

func NewScopeDiscoverer(arm ARMClient) ScopeDiscoverer {
	return ScopeDiscoverer{arm: arm}
}

type managementGroupsResponse struct {
	Value []managementGroup `json:"value"`
}

type managementGroup struct {
	Name string `json:"name"`
}

type subscriptionsResponse struct {
	Value []subscription `json:"value"`
}

type subscription struct {
	SubscriptionID string `json:"subscriptionId"`
}

type resourceGroupsResponse struct {
	Value []resourceGroup `json:"value"`
}

type resourceGroup struct {
	ID string `json:"id"`
}

func (d ScopeDiscoverer) Discover(ctx context.Context) ([]string, error) {
	var scopes []string

	var managementGroups managementGroupsResponse
	if err := d.arm.Get(ctx, "/providers/Microsoft.Management/managementGroups?api-version=2020-05-01", &managementGroups); err != nil {
		return nil, err
	}
	for _, group := range managementGroups.Value {
		scopes = append(scopes, "/providers/Microsoft.Management/managementGroups/"+group.Name)
	}

	var subscriptions subscriptionsResponse
	if err := d.arm.Get(ctx, "/subscriptions?api-version=2020-01-01", &subscriptions); err != nil {
		return nil, err
	}
	for _, sub := range subscriptions.Value {
		subScope := "/subscriptions/" + sub.SubscriptionID
		scopes = append(scopes, subScope)

		var resourceGroups resourceGroupsResponse
		path := fmt.Sprintf("/subscriptions/%s/resourcegroups?api-version=2021-04-01", sub.SubscriptionID)
		if err := d.arm.Get(ctx, path, &resourceGroups); err != nil {
			return nil, err
		}
		for _, rg := range resourceGroups.Value {
			scopes = append(scopes, rg.ID)
		}
	}

	return scopes, nil
}
