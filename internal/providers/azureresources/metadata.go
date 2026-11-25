package azureresources

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/pim"
)

const maxConcurrentScopeRequests = 8

type activeAssignmentResponse struct {
	Value    []roleAssignmentScheduleInstance `json:"value"`
	NextLink string                           `json:"nextLink"`
}

type roleAssignmentScheduleInstance struct {
	Properties roleAssignmentScheduleProperties `json:"properties"`
}

type roleAssignmentScheduleProperties struct {
	Scope                           string     `json:"scope"`
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
	ID                string   `json:"id"`
	RuleType          string   `json:"ruleType"`
	MaximumDuration   string   `json:"maximumDuration"`
	EnabledRules      []string `json:"enabledRules"`
	IsEnabled         bool     `json:"isEnabled"`
	Enabled           bool     `json:"enabled"`
	ClaimValue        string   `json:"claimValue"`
	CustomExtensionID string   `json:"customExtensionId"`
	CustomAttributes  []string `json:"customAttributes"`
	Setting           *struct {
		IsApprovalRequired               bool `json:"isApprovalRequired"`
		IsRequestorJustificationRequired bool `json:"isRequestorJustificationRequired"`
	} `json:"setting"`
	Target struct {
		Caller     string   `json:"caller"`
		Level      string   `json:"level"`
		Operations []string `json:"operations"`
	} `json:"target"`
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
	seen := make(map[string]bool)
	for _, rule := range assignment.Properties.EffectiveRules {
		if !isEndUserActivationRule(rule) {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(rule.ID))
		switch id {
		case "expiration_enduser_assignment", "enablement_enduser_assignment", "authenticationcontext_enduser_assignment", "approval_enduser_assignment", "customextension_preapproval_enduser_assignment", "customextension_postapproval_enduser_assignment":
			if seen[id] {
				return pim.ActivationPolicy{}, fmt.Errorf("duplicate Azure activation policy rule %q for role definition %s", rule.ID, roleDefinitionID)
			}
			seen[id] = true
		default:
			return pim.ActivationPolicy{}, fmt.Errorf("Azure activation requires unsupported policy rule %q for role definition %s; activate it in the Azure portal", rule.ID, roleDefinitionID)
		}
		switch id {
		case "expiration_enduser_assignment":
			policy.MaximumDurationISO = strings.TrimSpace(rule.MaximumDuration)
		case "authenticationcontext_enduser_assignment":
			if rule.IsEnabled {
				policy.AuthenticationContext = strings.TrimSpace(rule.ClaimValue)
				if policy.AuthenticationContext == "" {
					return pim.ActivationPolicy{}, fmt.Errorf("empty authentication context for role definition %s", roleDefinitionID)
				}
			}
		case "enablement_enduser_assignment":
			for _, enabled := range rule.EnabledRules {
				switch {
				case strings.EqualFold(enabled, "Justification"):
					policy.JustificationRequired = true
				case strings.EqualFold(enabled, "MultiFactorAuthentication"):
					policy.MFARequired = true
				default:
					return pim.ActivationPolicy{}, fmt.Errorf("Azure activation requires unsupported policy setting %q for role definition %s; activate it in the Azure portal", enabled, roleDefinitionID)
				}
			}
		case "approval_enduser_assignment":
			// Default policies set isRequestorJustificationRequired even with approval off; it only applies to approval requests.
			if rule.Setting != nil && rule.Setting.IsApprovalRequired && rule.Setting.IsRequestorJustificationRequired {
				policy.JustificationRequired = true
			}
		case "customextension_preapproval_enduser_assignment", "customextension_postapproval_enduser_assignment":
			// ARM uses "enabled" while Graph uses "isEnabled"; audit mode keeps enabled false with a linked customExtensionId.
			if (rule.Enabled || rule.IsEnabled || strings.TrimSpace(rule.CustomExtensionID) != "") && len(rule.CustomAttributes) > 0 {
				return pim.ActivationPolicy{}, fmt.Errorf("Azure activation requires unsupported custom extension attributes (%s) for role definition %s; activate it in the Azure portal", strings.Join(rule.CustomAttributes, ", "), roleDefinitionID)
			}
		}
	}
	if policy.MaximumDurationISO == "" || !seen["enablement_enduser_assignment"] {
		return pim.ActivationPolicy{}, fmt.Errorf("incomplete end-user activation policy for role definition %s", roleDefinitionID)
	}
	return policy, nil
}

func isEndUserActivationRule(rule roleManagementPolicyRule) bool {
	idLower := strings.ToLower(strings.TrimSpace(rule.ID))
	if strings.HasPrefix(idLower, "notification_") || strings.EqualFold(rule.RuleType, "RoleManagementPolicyNotificationRule") {
		return false
	}
	if rule.Target.Caller != "" && !strings.EqualFold(rule.Target.Caller, "EndUser") {
		return false
	}
	if rule.Target.Level != "" && !strings.EqualFold(rule.Target.Level, "Assignment") {
		return false
	}
	if len(rule.Target.Operations) > 0 && !slices.ContainsFunc(rule.Target.Operations, func(op string) bool {
		return strings.EqualFold(op, "All") || strings.EqualFold(op, "Activate")
	}) {
		return false
	}
	if rule.Target.Caller == "" && strings.Contains(idLower, "_admin_") {
		return false
	}
	if rule.Target.Level == "" && strings.HasSuffix(idLower, "_eligibility") {
		return false
	}
	return true
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

// forEach runs fn for indices [0, count) with bounded concurrency, cancels the
// remaining calls on the first error and returns that error.
func forEach(ctx context.Context, count int, fn func(context.Context, int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	semaphore := make(chan struct{}, maxConcurrentScopeRequests)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for index := range count {
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
			if err := fn(ctx, index); err != nil {
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
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%%28%%29&api-version=%s", scopePathPrefix(scope), arm.AuthorizationAPIVersion)
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

func scopedRoleDefinitionID(scope string, roleDefinitionID string) string {
	guid := resourceName(roleDefinitionID)
	if guid == "" {
		return ""
	}
	return fmt.Sprintf("%s/providers/Microsoft.Authorization/roleDefinitions/%s", scopePathPrefix(scope), guid)
}

func (p Provider) policiesForScope(ctx context.Context, scope string, roleFilter string) ([]roleManagementPolicyAssignment, error) {
	query := fmt.Sprintf("api-version=%s", arm.AuthorizationAPIVersion)
	if roleFilter != "" {
		query = fmt.Sprintf("$filter=%s&%s", url.QueryEscape(fmt.Sprintf("roleDefinitionId eq '%s'", roleFilter)), query)
	}
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleManagementPolicyAssignments?%s", scopePathPrefix(scope), query)
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
	bySchedule := make(map[string][]roleAssignmentScheduleInstance, len(active))
	for _, instance := range active {
		key := resourceName(instance.Properties.LinkedRoleEligibilityScheduleID)
		if key != "" && isCurrentActivation(instance, now) {
			bySchedule[key] = append(bySchedule[key], instance)
		}
	}
	for index := range assignments {
		key := resourceName(assignments[index].EligibilityScheduleID)
		if key == "" {
			continue
		}
		targetScope := scopePathPrefix(assignments[index].AzureScope)
		for _, instance := range bySchedule[key] {
			instanceScope := scopePathPrefix(instance.Properties.Scope)
			if instanceScope != "" && !strings.EqualFold(instanceScope, targetScope) {
				continue
			}
			assignments[index].Active = true
			assignments[index].ActiveUntil = instance.Properties.EndDateTime
			break
		}
	}
}

type policyLookup struct {
	scope      string
	roleFilter string
	indices    []int
	policies   []roleManagementPolicyAssignment
}

// applyPolicies loads one role-filtered policy per (scope, role); an unfiltered
// scope listing returns every role's policy and is only used without a role ID.
func (p Provider) applyPolicies(ctx context.Context, assignments []pim.EligibleAssignment) error {
	type lookupKey struct{ scope, role string }
	positions := make(map[lookupKey]int)
	var lookups []policyLookup
	for index := range assignments {
		scope := strings.TrimRight(strings.TrimSpace(assignments[index].AzureScope), "/")
		if scope == "" {
			continue
		}
		key := lookupKey{scope: strings.ToLower(scopePathPrefix(scope)), role: resourceName(assignments[index].RoleDefinitionID)}
		position, ok := positions[key]
		if !ok {
			position = len(lookups)
			positions[key] = position
			lookups = append(lookups, policyLookup{scope: scope, roleFilter: scopedRoleDefinitionID(scope, assignments[index].RoleDefinitionID)})
		}
		lookups[position].indices = append(lookups[position].indices, index)
	}
	if err := forEach(ctx, len(lookups), func(ctx context.Context, index int) error {
		policies, err := p.policiesForScope(ctx, lookups[index].scope, lookups[index].roleFilter)
		lookups[index].policies = policies
		return err
	}); err != nil {
		return err
	}
	for _, lookup := range lookups {
		for _, index := range lookup.indices {
			policy, err := policyForAssignment(lookup.policies, assignments[index].RoleDefinitionID)
			if err != nil {
				return fmt.Errorf("activation policy for %s at %s: %w", assignments[index].DisplayName, lookup.scope, err)
			}
			assignments[index].ActivationPolicy = policy
		}
	}
	return nil
}
