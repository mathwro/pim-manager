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
	Value    []managementGroup `json:"value"`
	NextLink string            `json:"nextLink"`
}

type managementGroup struct {
	Name string `json:"name"`
}

type subscriptionsResponse struct {
	Value    []subscription `json:"value"`
	NextLink string         `json:"nextLink"`
}

type subscription struct {
	SubscriptionID string `json:"subscriptionId"`
}

type resourceGroupsResponse struct {
	Value    []resourceGroup `json:"value"`
	NextLink string          `json:"nextLink"`
}

type resourceGroup struct {
	ID string `json:"id"`
}

func (d ScopeDiscoverer) Discover(ctx context.Context) ([]string, error) {
	var scopes []string

	managementGroupPath := "/providers/Microsoft.Management/managementGroups?api-version=2020-05-01"
	for managementGroupPath != "" {
		var managementGroups managementGroupsResponse
		if err := d.arm.Get(ctx, managementGroupPath, &managementGroups); err != nil {
			return nil, err
		}
		for _, group := range managementGroups.Value {
			scopes = append(scopes, "/providers/Microsoft.Management/managementGroups/"+group.Name)
		}
		managementGroupPath = managementGroups.NextLink
	}

	subscriptionPath := "/subscriptions?api-version=2020-01-01"
	for subscriptionPath != "" {
		var subscriptions subscriptionsResponse
		if err := d.arm.Get(ctx, subscriptionPath, &subscriptions); err != nil {
			return nil, err
		}
		for _, sub := range subscriptions.Value {
			subScope := "/subscriptions/" + sub.SubscriptionID
			scopes = append(scopes, subScope)

			resourceGroupPath := fmt.Sprintf("/subscriptions/%s/resourcegroups?api-version=2021-04-01", sub.SubscriptionID)
			for resourceGroupPath != "" {
				var resourceGroups resourceGroupsResponse
				if err := d.arm.Get(ctx, resourceGroupPath, &resourceGroups); err != nil {
					return nil, err
				}
				for _, rg := range resourceGroups.Value {
					scopes = append(scopes, rg.ID)
				}
				resourceGroupPath = resourceGroups.NextLink
			}
		}
		subscriptionPath = subscriptions.NextLink
	}

	return scopes, nil
}
