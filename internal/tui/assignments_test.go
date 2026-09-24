package tui

import (
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestAssignmentListFiltersByRoleAndScope(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{
		{ID: "one", DisplayName: "Contributor", Scope: pim.Scope{DisplayName: "rg-prod"}},
		{ID: "two", DisplayName: "Global Reader", Scope: pim.Scope{DisplayName: "Tenant"}},
	})

	filtered := list.filtered("prod")
	if len(filtered) != 1 || filtered[0].ID != "one" {
		t.Fatalf("expected rg-prod assignment, got %#v", filtered)
	}
}

func TestAssignmentListFiltersBySubscriptionAndManagementGroupIdentity(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{
		{ID: "one", DisplayName: "Reader", Scope: pim.Scope{Type: pim.ScopeTypeSubscription, DisplayName: "Production", ID: "/subscriptions/12345678-abcd-4321-9876-abcdef012345"}},
		{ID: "two", DisplayName: "Reader", Scope: pim.Scope{Type: pim.ScopeTypeManagementGroup, DisplayName: "Platform", ID: "/providers/Microsoft.Management/managementGroups/mg-platform-core"}},
	})

	for _, test := range []struct {
		query string
		want  string
	}{
		{query: "production", want: "one"},
		{query: "ABCDEF012345", want: "one"},
		{query: "platform", want: "two"},
		{query: "MG-PLATFORM-CORE", want: "two"},
	} {
		t.Run(test.query, func(t *testing.T) {
			filtered := list.filtered(test.query)
			if len(filtered) != 1 || filtered[0].ID != test.want {
				t.Fatalf("filtered(%q) = %#v, want assignment %q", test.query, filtered, test.want)
			}
		})
	}
}

func TestAssignmentListTogglesSelection(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{{ID: "one", DisplayName: "Contributor"}})
	list.toggle("one")

	selected := list.selected()
	if len(selected) != 1 || selected[0].ID != "one" {
		t.Fatalf("expected selected assignment, got %#v", selected)
	}
}

func TestAssignmentListDoesNotSelectActiveAssignment(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{
		{ID: "inactive", DisplayName: "Contributor"},
		{ID: "active", DisplayName: "Owner", Active: true},
	})

	list.toggle("active")
	list.toggle("inactive")

	selected := list.selected()
	if len(selected) != 1 || selected[0].ID != "inactive" {
		t.Fatalf("expected only inactive assignment selected, got %#v", selected)
	}
}

func TestAssignmentListFiltersByActiveState(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{
		{ID: "inactive", DisplayName: "Contributor"},
		{ID: "inactive-collision", DisplayName: "Active Directory Administrator"},
		{ID: "active", DisplayName: "Inactive Owner", Active: true},
	})

	filtered := list.filtered("active")
	if len(filtered) != 1 || filtered[0].ID != "active" {
		t.Fatalf("expected only active assignment, got %#v", filtered)
	}

	filtered = list.filtered("inactive")
	if len(filtered) != 2 || filtered[0].ID != "inactive" || filtered[1].ID != "inactive-collision" {
		t.Fatalf("expected only inactive assignments, got %#v", filtered)
	}
}
