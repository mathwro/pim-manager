package groups

import (
	"context"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestDiscoverFetchesEligibilitiesWithPrincipalFilter(t *testing.T) {
	graph := &fakeGraph{response: eligibilityResponse{Value: []eligibilityScheduleInstance{{
		ID:          "group-1_member_sched-1",
		AccessID:    "member",
		PrincipalID: "principal-1",
		GroupID:     "group-1",
	}}}}
	provider := NewProvider(graph, "principal-1")

	got, err := provider.Discover(context.Background())

	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	const expectedPath = "/identityGovernance/privilegedAccess/group/eligibilityScheduleInstances?$filter=principalId+eq+%27principal-1%27"
	if graph.getPath != expectedPath {
		t.Fatalf("expected Get path %q, got %q", expectedPath, graph.getPath)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(got))
	}
	if got[0].ID != "group-1_member_sched-1" || got[0].Source != pim.AssignmentSourceGroup || got[0].Kind != pim.AssignmentKindGroupMember {
		t.Fatalf("unexpected assignment: %#v", got[0])
	}
	if got[0].PrincipalID != "principal-1" || got[0].GroupID != "group-1" || got[0].AccessID != "member" {
		t.Fatalf("assignment was not normalized from response: %#v", got[0])
	}
}

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

type fakeGraph struct {
	getPath  string
	response eligibilityResponse
}

func (f *fakeGraph) Get(_ context.Context, path string, out any) error {
	f.getPath = path
	response := out.(*eligibilityResponse)
	*response = f.response
	return nil
}

func (f *fakeGraph) Post(context.Context, string, any, any) error { return nil }
