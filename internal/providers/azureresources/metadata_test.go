package azureresources

import (
	"strings"
	"testing"
	"time"
)

func TestIsCurrentActivation(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	tests := []struct {
		name string
		item roleAssignmentScheduleInstance
		want bool
	}{
		{
			name: "current",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Activated", Status: "Provisioned", StartDateTime: &past, EndDateTime: &future,
			}},
			want: true,
		},
		{
			name: "upcoming",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Activated", Status: "Provisioned", StartDateTime: &future,
			}},
		},
		{
			name: "expired",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Activated", Status: "Provisioned", EndDateTime: &past,
			}},
		},
		{
			name: "revoked",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Activated", Status: "Revoked", StartDateTime: &past, EndDateTime: &future,
			}},
		},
		{
			name: "not activated",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Assigned", Status: "Provisioned", StartDateTime: &past, EndDateTime: &future,
			}},
		},
		{
			name: "failed terminal state",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Activated", Status: "FailedAsResourceIsLocked", StartDateTime: &past, EndDateTime: &future,
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isCurrentActivation(test.item, now); got != test.want {
				t.Fatalf("expected %v, got %v", test.want, got)
			}
		})
	}
}

func TestPolicyForAssignmentUsesEffectiveEndUserRules(t *testing.T) {
	policy, err := policyForAssignment([]roleManagementPolicyAssignment{{
		Properties: roleManagementPolicyAssignmentProperties{
			RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/OWNER",
			EffectiveRules: []roleManagementPolicyRule{
				{ID: "Expiration_EndUser_Assignment", MaximumDuration: "PT8H"},
				{ID: "Enablement_EndUser_Assignment", EnabledRules: []string{"MultiFactorAuthentication", "Justification"}},
			},
		},
	}}, "/subscriptions/sub-1/providers/microsoft.authorization/roledefinitions/owner")
	if err != nil {
		t.Fatalf("policyForAssignment returned error: %v", err)
	}
	if policy.MaximumDurationISO != "PT8H" || !policy.JustificationRequired {
		t.Fatalf("unexpected policy: %#v", policy)
	}
}

func testPolicy(scope, role, maximum string, enabled ...string) roleManagementPolicyAssignment {
	return roleManagementPolicyAssignment{Properties: roleManagementPolicyAssignmentProperties{
		Scope:            scope,
		RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/" + role,
		EffectiveRules: []roleManagementPolicyRule{
			{ID: "Expiration_EndUser_Assignment", MaximumDuration: maximum},
			{ID: "Enablement_EndUser_Assignment", EnabledRules: enabled},
		},
	}}
}

func TestPolicyForAssignmentAllowsOptionalJustification(t *testing.T) {
	policy, err := policyForAssignment(
		[]roleManagementPolicyAssignment{testPolicy("/subscriptions/sub-1", "reader", "PT8H")},
		"/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader",
	)
	if err != nil || policy.JustificationRequired || policy.MaximumDurationISO != "PT8H" {
		t.Fatalf("expected optional justification policy, got %#v, %v", policy, err)
	}
}

func TestPolicyForAssignmentRejectsMissingMaximum(t *testing.T) {
	_, err := policyForAssignment([]roleManagementPolicyAssignment{{Properties: roleManagementPolicyAssignmentProperties{
		RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader",
		EffectiveRules:   []roleManagementPolicyRule{{ID: "Enablement_EndUser_Assignment"}},
	}}}, "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader")
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete policy error, got %v", err)
	}
}

func TestPolicyForAssignmentRejectsMissingPolicy(t *testing.T) {
	_, err := policyForAssignment(nil, "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader")
	if err == nil || !strings.Contains(err.Error(), "no activation policy") {
		t.Fatalf("expected missing policy error, got %v", err)
	}
}

func TestPolicyForAssignmentRejectsMissingEnablement(t *testing.T) {
	roleDefinitionID := "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader"
	_, err := policyForAssignment([]roleManagementPolicyAssignment{{Properties: roleManagementPolicyAssignmentProperties{
		RoleDefinitionID: roleDefinitionID,
		EffectiveRules:   []roleManagementPolicyRule{{ID: "Expiration_EndUser_Assignment", MaximumDuration: "PT8H"}},
	}}}, roleDefinitionID)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete policy error, got %v", err)
	}
}

func TestPolicyForAssignmentRejectsAmbiguousMatches(t *testing.T) {
	roleDefinitionID := "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader"
	_, err := policyForAssignment([]roleManagementPolicyAssignment{
		testPolicy("/subscriptions/sub-1", "reader", "PT8H"),
		testPolicy("/subscriptions/SUB-1", "READER", "PT4H", "Justification"),
	}, roleDefinitionID)
	if err == nil || !strings.Contains(err.Error(), "multiple activation policies") || !strings.Contains(err.Error(), roleDefinitionID) {
		t.Fatalf("expected role-specific ambiguity error, got %v", err)
	}
}
