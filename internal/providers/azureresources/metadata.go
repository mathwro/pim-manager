package azureresources

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/pim"
)

const maxConcurrentScopeRequests = 4

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
	IsEnabled       bool     `json:"isEnabled"`
	ClaimValue      string   `json:"claimValue"`
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
		case strings.EqualFold(rule.ID, "AuthenticationContext_EndUser_Assignment") && rule.IsEnabled:
			policy.AuthenticationContext = strings.TrimSpace(rule.ClaimValue)
			if policy.AuthenticationContext == "" {
				return pim.ActivationPolicy{}, fmt.Errorf("empty authentication context for role definition %s", roleDefinitionID)
			}
		case strings.EqualFold(rule.ID, "Enablement_EndUser_Assignment"):
			enablementFound = true
			for _, enabled := range rule.EnabledRules {
				switch {
				case strings.EqualFold(enabled, "Justification"):
					policy.JustificationRequired = true
				case strings.EqualFold(enabled, "MultiFactorAuthentication"):
					policy.MFARequired = true
				}
			}
		}
	}
	if policy.MaximumDurationISO == "" || !enablementFound {
		return pim.ActivationPolicy{}, fmt.Errorf("incomplete end-user activation policy for role definition %s", roleDefinitionID)
	}
	return policy, nil
}

func assignmentScopes(assignments []pim.EligibleAssignment) []string {
	byKey := make(map[string]string)
	for _, assignment := range assignments {
		scope := strings.TrimRight(strings.TrimSpace(assignment.AzureScope), "/")
		if scope == "" {
			continue
		}
		key := strings.ToLower(scope)
		if _, exists := byKey[key]; !exists {
			byKey[key] = scope
		}
	}
	scopes := make([]string, 0, len(byKey))
	for _, scope := range byKey {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
}

func forEachScope(ctx context.Context, scopes []string, fn func(context.Context, int, string) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	semaphore := make(chan struct{}, maxConcurrentScopeRequests)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for index, scope := range scopes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			if err := fn(ctx, index, scope); err != nil {
				select {
				case errCh <- err:
				default:
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
}

func (p Provider) discoverActiveAssignments(ctx context.Context, scope string) ([]roleAssignmentScheduleInstance, error) {
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%%28%%29&api-version=%s", strings.TrimRight(scope, "/"), arm.AuthorizationAPIVersion)
	var out []roleAssignmentScheduleInstance
	for path != "" {
		var response activeAssignmentResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("list active Azure role assignments at %s: %w", scope, err)
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
		key := strings.ToLower(strings.TrimRight(strings.TrimSpace(assignments[index].AzureScope), "/"))
		byScope[key] = append(byScope[key], index)
	}
	scopes := assignmentScopes(assignments)
	policiesByScope := make([][]roleManagementPolicyAssignment, len(scopes))
	if err := forEachScope(ctx, scopes, func(ctx context.Context, index int, scope string) error {
		policies, err := p.policiesForScope(ctx, scope)
		policiesByScope[index] = policies
		return err
	}); err != nil {
		return err
	}
	for scopeIndex, scope := range scopes {
		for _, assignmentIndex := range byScope[strings.ToLower(scope)] {
			policy, err := policyForAssignment(policiesByScope[scopeIndex], assignments[assignmentIndex].RoleDefinitionID)
			if err != nil {
				return fmt.Errorf("activation policy for %s at %s: %w", assignments[assignmentIndex].DisplayName, scope, err)
			}
			assignments[assignmentIndex].ActivationPolicy = policy
		}
	}
	return nil
}
