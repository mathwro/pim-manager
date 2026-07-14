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

func TestAssignmentListTogglesSelection(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{{ID: "one", DisplayName: "Contributor"}})
	list.toggle("one")

	selected := list.selected()
	if len(selected) != 1 || selected[0].ID != "one" {
		t.Fatalf("expected selected assignment, got %#v", selected)
	}
}
