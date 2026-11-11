package graph

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/mathwro/pim-manager/internal/pim"
)

type policyReader interface {
	Get(context.Context, string, any) error
}

type policyAssignmentResponse struct {
	Value    []policyAssignment `json:"value"`
	NextLink string             `json:"@odata.nextLink"`
}

type policyAssignment struct {
	PolicyID         string               `json:"policyId"`
	ScopeID          string               `json:"scopeId"`
	ScopeType        string               `json:"scopeType"`
	RoleDefinitionID string               `json:"roleDefinitionId"`
	Policy           roleManagementPolicy `json:"policy"`
}

type roleManagementPolicy struct {
	ID                     string       `json:"id"`
	EffectiveRules         []policyRule `json:"effectiveRules"`
	EffectiveRulesNextLink string       `json:"effectiveRules@odata.nextLink"`
}

type policyRuleResponse struct {
	Value    []policyRule `json:"value"`
	NextLink string       `json:"@odata.nextLink"`
}

type policyRule struct {
	ID              string   `json:"id"`
	MaximumDuration string   `json:"maximumDuration"`
	EnabledRules    []string `json:"enabledRules"`
	IsEnabled       *bool    `json:"isEnabled"`
	ClaimValue      string   `json:"claimValue"`
	Setting         *struct {
		IsApprovalRequired               bool `json:"isApprovalRequired"`
		IsRequestorJustificationRequired bool `json:"isRequestorJustificationRequired"`
	} `json:"setting"`
	Target struct {
		Caller     string   `json:"caller"`
		Level      string   `json:"level"`
		Operations []string `json:"operations"`
	} `json:"target"`
}

type policyTarget struct {
	scopeID   string
	scopeType string
	roleID    string
}

// PrepareAssignments leaves discovery/cache values untouched and never invents activation defaults.
func PrepareAssignments(ctx context.Context, client policyReader, assignments []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	prepared := slices.Clone(assignments)
	policies := make(map[policyTarget]pim.ActivationPolicy)
	for index := range prepared {
		assignment := &prepared[index]
		target := policyTarget{scopeID: "/", scopeType: "DirectoryRole", roleID: assignment.RoleDefinitionID}
		if assignment.Source == pim.AssignmentSourceGroup || assignment.GroupID != "" {
			target = policyTarget{scopeID: assignment.GroupID, scopeType: "Group", roleID: assignment.AccessID}
			if target.scopeID == "" || (target.roleID != "member" && target.roleID != "owner") {
				return nil, fmt.Errorf("activation policy for %s: missing group or unsupported access type %q", assignment.DisplayName, target.roleID)
			}
		}
		if target.roleID == "" {
			return nil, fmt.Errorf("activation policy for %s: missing role definition", assignment.DisplayName)
		}
		policy, found := policies[target]
		if !found {
			var err error
			policy, err = loadActivationPolicy(ctx, client, target)
			if err != nil {
				return nil, fmt.Errorf("activation policy for %s (%s) at %s: %w", assignment.DisplayName, target.roleID, target.scopeID, err)
			}
			policies[target] = policy
		}
		assignment.ActivationPolicy = policy
	}
	return prepared, nil
}

func loadActivationPolicy(ctx context.Context, client policyReader, target policyTarget) (pim.ActivationPolicy, error) {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	filter := "scopeId eq " + quote(target.scopeID) + " and scopeType eq " + quote(target.scopeType) + " and roleDefinitionId eq " + quote(target.roleID)
	path := "/policies/roleManagementPolicyAssignments?$filter=" + url.QueryEscape(filter) + "&$expand=policy($expand=effectiveRules)"
	var matching policyAssignment
	matches := 0
	for path != "" {
		var response policyAssignmentResponse
		if err := client.Get(ctx, path, &response); err != nil {
			return pim.ActivationPolicy{}, fmt.Errorf("read Graph role management policy assignments: %w", err)
		}
		for _, assignment := range response.Value {
			if strings.EqualFold(assignment.ScopeID, target.scopeID) && strings.EqualFold(assignment.ScopeType, target.scopeType) && strings.EqualFold(assignment.RoleDefinitionID, target.roleID) {
				matching = assignment
				matches++
			}
		}
		path = response.NextLink
	}
	if matches != 1 {
		return pim.ActivationPolicy{}, fmt.Errorf("expected one matching Graph activation policy, found %d", matches)
	}
	assignment := matching
	if assignment.PolicyID != "" && assignment.Policy.ID != "" && assignment.PolicyID != assignment.Policy.ID {
		return pim.ActivationPolicy{}, fmt.Errorf("expanded Graph policy does not match policy assignment")
	}
	rules := assignment.Policy.EffectiveRules
	path = assignment.Policy.EffectiveRulesNextLink
	if rules == nil {
		if assignment.PolicyID == "" {
			return pim.ActivationPolicy{}, fmt.Errorf("Graph policy assignment has neither rules nor policy ID")
		}
		path = "/policies/roleManagementPolicies/" + url.PathEscape(assignment.PolicyID) + "/effectiveRules"
	}
	for path != "" {
		var response policyRuleResponse
		if err := client.Get(ctx, path, &response); err != nil {
			return pim.ActivationPolicy{}, fmt.Errorf("read Graph activation policy rules: %w", err)
		}
		rules = append(rules, response.Value...)
		path = response.NextLink
	}
	return normalizeActivationPolicy(rules)
}

func normalizeActivationPolicy(rules []policyRule) (pim.ActivationPolicy, error) {
	var policy pim.ActivationPolicy
	seen := make(map[string]bool)
	for _, rule := range rules {
		if !strings.EqualFold(rule.Target.Caller, "EndUser") || !strings.EqualFold(rule.Target.Level, "Assignment") {
			continue
		}
		if !slices.ContainsFunc(rule.Target.Operations, func(operation string) bool {
			return strings.EqualFold(operation, "All") || strings.EqualFold(operation, "Activate")
		}) {
			continue
		}
		id := strings.ToLower(rule.ID)
		switch id {
		case "expiration_enduser_assignment", "enablement_enduser_assignment", "authenticationcontext_enduser_assignment", "approval_enduser_assignment":
			if seen[id] {
				return pim.ActivationPolicy{}, fmt.Errorf("duplicate Graph activation policy rule %s", rule.ID)
			}
			seen[id] = true
		default:
			// Notification rules only send email; any other applicable rule is a requirement this client cannot satisfy.
			if strings.HasPrefix(id, "notification_") {
				continue
			}
			return pim.ActivationPolicy{}, fmt.Errorf("Graph activation requires unsupported policy rule %s; activate it in the Microsoft Entra admin center", rule.ID)
		}
		switch id {
		case "expiration_enduser_assignment":
			policy.MaximumDurationISO = strings.TrimSpace(rule.MaximumDuration)
		case "enablement_enduser_assignment":
			if rule.EnabledRules == nil {
				return pim.ActivationPolicy{}, fmt.Errorf("Graph activation policy enablement settings are missing")
			}
			for _, enabled := range rule.EnabledRules {
				switch strings.ToLower(enabled) {
				case "justification":
					policy.JustificationRequired = true
				case "multifactorauthentication":
					policy.MFARequired = true
				default:
					return pim.ActivationPolicy{}, fmt.Errorf("Graph activation requires unsupported policy setting %q", enabled)
				}
			}
		case "authenticationcontext_enduser_assignment":
			if rule.IsEnabled == nil {
				return pim.ActivationPolicy{}, fmt.Errorf("Graph activation policy authentication context state is missing")
			}
			if *rule.IsEnabled {
				policy.AuthenticationContext = strings.TrimSpace(rule.ClaimValue)
				if policy.AuthenticationContext == "" {
					return pim.ActivationPolicy{}, fmt.Errorf("Graph activation policy has an enabled but empty authentication context")
				}
			}
		case "approval_enduser_assignment":
			// Default policies set isRequestorJustificationRequired even with approval off; it only applies to approval requests.
			if rule.Setting != nil && rule.Setting.IsApprovalRequired && rule.Setting.IsRequestorJustificationRequired {
				policy.JustificationRequired = true
			}
		}
	}
	if policy.MaximumDurationISO == "" || !seen["enablement_enduser_assignment"] || !seen["authenticationcontext_enduser_assignment"] {
		return pim.ActivationPolicy{}, fmt.Errorf("incomplete Graph end-user activation policy")
	}
	return policy, nil
}
