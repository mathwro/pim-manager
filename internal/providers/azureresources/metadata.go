package azureresources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/pim"
)

type activeAssignmentResponse struct {
	Value    []roleAssignmentScheduleInstance `json:"value"`
	NextLink string                           `json:"nextLink"`
}

type roleAssignmentScheduleInstance struct {
	Properties roleAssignmentScheduleProperties `json:"properties"`
}

type roleAssignmentScheduleProperties struct {
	LinkedRoleEligibilityScheduleID string     `json:"linkedRoleEligibilityScheduleId"`
	AssignmentType                  string     `json:"assignmentType"`
	Status                          string     `json:"status"`
	StartDateTime                   *time.Time `json:"startDateTime"`
	EndDateTime                     *time.Time `json:"endDateTime"`
}

type policyAssignmentResponse struct {
	Value    []roleManagementPolicyAssignment `json:"value"`
	NextLink string                           `json:"nextLink"`
}

type roleManagementPolicyAssignment struct {
	Properties roleManagementPolicyAssignmentProperties `json:"properties"`
}

type roleManagementPolicyAssignmentProperties struct {
	Scope            string                     `json:"scope"`
	RoleDefinitionID string                     `json:"roleDefinitionId"`
	EffectiveRules   []roleManagementPolicyRule `json:"effectiveRules"`
}

type roleManagementPolicyRule struct {
	ID              string   `json:"id"`
	RuleType        string   `json:"ruleType"`
	MaximumDuration string   `json:"maximumDuration"`
	EnabledRules    []string `json:"enabledRules"`
}

func resourceName(id string) string {
	id = strings.TrimRight(strings.TrimSpace(id), "/")
	if index := strings.LastIndexByte(id, '/'); index >= 0 {
		id = id[index+1:]
	}
	return strings.ToLower(id)
}

func isCurrentActivation(item roleAssignmentScheduleInstance, now time.Time) bool {
	properties := item.Properties
	if !strings.EqualFold(properties.AssignmentType, "Activated") {
		return false
	}
	switch strings.ToLower(properties.Status) {
	case "denied", "revoked", "canceled", "failed", "timedout", "invalid", "admindenied", "failedasresourceislocked":
		return false
	}
	if properties.StartDateTime != nil && properties.StartDateTime.After(now) {
		return false
	}
	return properties.EndDateTime == nil || properties.EndDateTime.After(now)
}

func policyForAssignment(policies []roleManagementPolicyAssignment, roleDefinitionID string) (pim.ActivationPolicy, error) {
	roleName := resourceName(roleDefinitionID)
	matchingIndex := -1
	matches := 0
	for index := range policies {
		if resourceName(policies[index].Properties.RoleDefinitionID) == roleName {
			matchingIndex = index
			matches++
		}
	}
	if matches == 0 {
		return pim.ActivationPolicy{}, fmt.Errorf("no activation policy for role definition %s", roleDefinitionID)
	}
	if matches > 1 {
		return pim.ActivationPolicy{}, fmt.Errorf("multiple activation policies for role definition %s", roleDefinitionID)
	}

	assignment := policies[matchingIndex]
	var policy pim.ActivationPolicy
	var enablementFound bool
	for _, rule := range assignment.Properties.EffectiveRules {
		switch {
		case strings.EqualFold(rule.ID, "Expiration_EndUser_Assignment"):
			policy.MaximumDurationISO = strings.TrimSpace(rule.MaximumDuration)
		case strings.EqualFold(rule.ID, "Enablement_EndUser_Assignment"):
			enablementFound = true
			for _, enabled := range rule.EnabledRules {
				if strings.EqualFold(enabled, "Justification") {
					policy.JustificationRequired = true
				}
			}
		}
	}
	if policy.MaximumDurationISO == "" || !enablementFound {
		return pim.ActivationPolicy{}, fmt.Errorf("incomplete end-user activation policy for role definition %s", roleDefinitionID)
	}
	return policy, nil
}

func (p Provider) discoverActiveAssignments(ctx context.Context) ([]roleAssignmentScheduleInstance, error) {
	path := fmt.Sprintf("/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%%28%%29&api-version=%s", arm.AuthorizationAPIVersion)
	var out []roleAssignmentScheduleInstance
	for path != "" {
		var response activeAssignmentResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("list active Azure role assignments: %w", err)
		}
		out = append(out, response.Value...)
		path = response.NextLink
	}
	return out, nil
}

func (p Provider) policiesForScope(ctx context.Context, scope string) ([]roleManagementPolicyAssignment, error) {
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=%s", strings.TrimRight(scope, "/"), arm.AuthorizationAPIVersion)
	var out []roleManagementPolicyAssignment
	for path != "" {
		var response policyAssignmentResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("list Azure activation policies at %s: %w", scope, err)
		}
		out = append(out, response.Value...)
		path = response.NextLink
	}
	return out, nil
}

func applyActiveState(assignments []pim.EligibleAssignment, active []roleAssignmentScheduleInstance, now time.Time) {
	bySchedule := make(map[string]roleAssignmentScheduleInstance, len(active))
	for _, instance := range active {
		key := resourceName(instance.Properties.LinkedRoleEligibilityScheduleID)
		if key != "" && isCurrentActivation(instance, now) {
			bySchedule[key] = instance
		}
	}
	for index := range assignments {
		key := resourceName(assignments[index].EligibilityScheduleID)
		if key == "" {
			continue
		}
		instance, ok := bySchedule[key]
		if !ok {
			continue
		}
		assignments[index].Active = true
		assignments[index].ActiveUntil = instance.Properties.EndDateTime
	}
}

func (p Provider) applyPolicies(ctx context.Context, assignments []pim.EligibleAssignment) error {
	byScope := make(map[string][]int)
	for index := range assignments {
		key := strings.ToLower(strings.TrimRight(assignments[index].AzureScope, "/"))
		byScope[key] = append(byScope[key], index)
	}
	for _, indexes := range byScope {
		scope := assignments[indexes[0]].AzureScope
		policies, err := p.policiesForScope(ctx, scope)
		if err != nil {
			return err
		}
		for _, index := range indexes {
			policy, err := policyForAssignment(policies, assignments[index].RoleDefinitionID)
			if err != nil {
				return fmt.Errorf("activation policy for %s at %s: %w", assignments[index].DisplayName, scope, err)
			}
			assignments[index].ActivationPolicy = policy
		}
	}
	return nil
}
