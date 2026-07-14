package groups

import (
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestNormalizeMemberEligibility(t *testing.T) {
	item := eligibilityScheduleInstance{
		ID:          "group-1_member_sched-1",
		AccessID:    "member",
		PrincipalID: "principal-1",
		GroupID:     "group-1",
	}

	got := normalizeEligibility(item)

	if got.Source != pim.AssignmentSourceGroup {
		t.Fatalf("expected group source, got %s", got.Source)
	}
	if got.Kind != pim.AssignmentKindGroupMember {
		t.Fatalf("expected group member kind, got %s", got.Kind)
	}
	if got.AccessID != "member" {
		t.Fatalf("expected member access ID, got %q", got.AccessID)
	}
}

func TestNormalizeOwnerEligibility(t *testing.T) {
	item := eligibilityScheduleInstance{ID: "group-1_owner_sched-1", AccessID: "owner", PrincipalID: "principal-1", GroupID: "group-1"}
	got := normalizeEligibility(item)
	if got.Kind != pim.AssignmentKindGroupOwner {
		t.Fatalf("expected group owner kind, got %s", got.Kind)
	}
}

func TestActivationRequestBody(t *testing.T) {
	body := activationBody(pim.ActivationRequest{
		Assignment:    pim.EligibleAssignment{PrincipalID: "principal-1", GroupID: "group-1", AccessID: "owner"},
		Justification: "Need ownership",
		DurationISO:   "PT1H",
	})

	if body.Action != "selfActivate" || body.AccessID != "owner" || body.ScheduleInfo.Expiration.Duration != "PT1H" {
		t.Fatalf("unexpected body: %#v", body)
	}
}
