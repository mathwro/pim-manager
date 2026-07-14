package entra

import (
	"context"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestNormalizeEligibility(t *testing.T) {
	item := roleEligibilitySchedule{
		ID:               "eligibility-instance-1",
		PrincipalID:      "principal-1",
		RoleDefinitionID: "role-1",
		DirectoryScopeID: "/",
		RoleDefinition: roleDefinition{
			DisplayName: "Global Reader",
		},
	}

	got := normalizeEligibility(item)

	if got.Source != pim.AssignmentSourceEntra {
		t.Fatalf("expected Entra source, got %s", got.Source)
	}
	if got.Kind != pim.AssignmentKindDirectoryRole {
		t.Fatalf("expected directory role kind, got %s", got.Kind)
	}
	if got.DisplayName != "Global Reader" {
		t.Fatalf("expected role display name, got %q", got.DisplayName)
	}
	if got.Scope.Type != pim.ScopeTypeTenant {
		t.Fatalf("expected tenant scope, got %s", got.Scope.Type)
	}
}

func TestActivationRequestBody(t *testing.T) {
	request := pim.ActivationRequest{
		Assignment: pim.EligibleAssignment{
			PrincipalID:      "principal-1",
			RoleDefinitionID: "role-1",
			DirectoryScopeID: "/",
		},
		Justification: "Need access",
		DurationISO:   "PT2H",
	}

	body := activationBody(request)

	if body.Action != "selfActivate" {
		t.Fatalf("expected selfActivate, got %q", body.Action)
	}
	if body.ScheduleInfo.Expiration.Type != "AfterDuration" {
		t.Fatalf("expected AfterDuration, got %q", body.ScheduleInfo.Expiration.Type)
	}
	if body.ScheduleInfo.Expiration.Duration != "PT2H" {
		t.Fatalf("expected duration PT2H, got %q", body.ScheduleInfo.Expiration.Duration)
	}
}

func TestMapStatus(t *testing.T) {
	provider := Provider{}
	result := provider.mapStatus(pim.EligibleAssignment{ID: "one"}, "PendingApproval", "")
	if result.Status != pim.ActivationStatusPendingApproval {
		t.Fatalf("expected pending approval, got %#v", result)
	}
}

func TestDiscoverFetchesAllPages(t *testing.T) {
	graph := &pagingFakeGraph{
		responses: []roleEligibilityResponse{
			{
				Value: []roleEligibilitySchedule{{
					ID:               "eligibility-1",
					PrincipalID:      "principal-1",
					RoleDefinitionID: "role-1",
					RoleDefinition:   roleDefinition{DisplayName: "First Role"},
				}},
				NextLink: "https://graph.microsoft.com/v1.0/next",
			},
			{
				Value: []roleEligibilitySchedule{{
					ID:               "eligibility-2",
					PrincipalID:      "principal-1",
					RoleDefinitionID: "role-2",
					RoleDefinition:   roleDefinition{DisplayName: "Second Role"},
				}},
			},
		},
	}
	provider := NewProvider(graph)

	got, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(got))
	}
	if got[0].ID != "eligibility-1" || got[1].ID != "eligibility-2" {
		t.Fatalf("unexpected assignments: %#v", got)
	}
	if len(graph.paths) != 2 {
		t.Fatalf("expected 2 graph requests, got %d (%v)", len(graph.paths), graph.paths)
	}
	if graph.paths[0] != "/roleManagement/directory/roleEligibilitySchedules/filterByCurrentUser(on='principal')?$expand=roleDefinition" {
		t.Fatalf("unexpected first path: %q", graph.paths[0])
	}
	if graph.paths[1] != "https://graph.microsoft.com/v1.0/next" {
		t.Fatalf("unexpected second path: %q", graph.paths[1])
	}
}

type fakeGraph struct{}

func (fakeGraph) Get(context.Context, string, any) error       { return nil }
func (fakeGraph) Post(context.Context, string, any, any) error { return nil }

type pagingFakeGraph struct {
	paths     []string
	responses []roleEligibilityResponse
}

func (f *pagingFakeGraph) Get(_ context.Context, path string, out any) error {
	f.paths = append(f.paths, path)
	response := out.(*roleEligibilityResponse)
	*response = f.responses[len(f.paths)-1]
	return nil
}

func (f *pagingFakeGraph) Post(context.Context, string, any, any) error { return nil }
