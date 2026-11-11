package groups

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/graph"
	"github.com/mathwro/pim-manager/internal/pim"
)

func TestDiscoverFetchesEligibilitiesForCurrentUser(t *testing.T) {
	graph := &fakeGraph{response: eligibilityResponse{Value: []eligibilityScheduleInstance{{
		ID:          "group-1_member_sched-1",
		AccessID:    "member",
		PrincipalID: "principal-1",
		GroupID:     "group-1",
	}}}}
	provider := NewProvider(graph)

	got, err := provider.Discover(context.Background())

	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	const expectedPath = "/identityGovernance/privilegedAccess/group/eligibilityScheduleInstances/filterByCurrentUser(on='principal')?$expand=group"
	const expectedActivePath = "/identityGovernance/privilegedAccess/group/assignmentScheduleInstances/filterByCurrentUser(on='principal')"
	if len(graph.paths) != 2 || graph.paths[0] != expectedPath || graph.paths[1] != expectedActivePath {
		t.Fatalf("expected Get paths [%q, %q], got %v", expectedPath, expectedActivePath, graph.paths)
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

func TestDiscoverFetchesAllPages(t *testing.T) {
	graph := &pagingFakeGraph{
		eligibilityPages: []eligibilityResponse{
			{
				Value: []eligibilityScheduleInstance{{
					ID:          "group-1_member_sched-1",
					AccessID:    "member",
					PrincipalID: "principal-1",
					GroupID:     "group-1",
				}},
				NextLink: "https://graph.microsoft.com/v1.0/next",
			},
			{
				Value: []eligibilityScheduleInstance{{
					ID:          "group-2_owner_sched-1",
					AccessID:    "owner",
					PrincipalID: "principal-1",
					GroupID:     "group-2",
				}},
			},
		},
		activePages: []assignmentInstanceResponse{{}},
	}
	provider := NewProvider(graph)

	got, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(got))
	}
	if got[0].ID != "group-1_member_sched-1" || got[1].ID != "group-2_owner_sched-1" {
		t.Fatalf("unexpected assignments: %#v", got)
	}
	if len(graph.paths) != 3 {
		t.Fatalf("expected 3 graph requests, got %d (%v)", len(graph.paths), graph.paths)
	}
	if graph.paths[0] != "/identityGovernance/privilegedAccess/group/eligibilityScheduleInstances/filterByCurrentUser(on='principal')?$expand=group" {
		t.Fatalf("unexpected first path: %q", graph.paths[0])
	}
	if graph.paths[1] != "https://graph.microsoft.com/v1.0/next" {
		t.Fatalf("unexpected second path: %q", graph.paths[1])
	}
	if graph.paths[2] != "/identityGovernance/privilegedAccess/group/assignmentScheduleInstances/filterByCurrentUser(on='principal')" {
		t.Fatalf("unexpected active path: %q", graph.paths[2])
	}
}

func TestDiscoverSkipsActiveLookupWhenNoGroupEligibilities(t *testing.T) {
	graph := &pagingFakeGraph{
		eligibilityPages: []eligibilityResponse{{}},
	}
	got, err := NewProvider(graph).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 assignments, got %d", len(got))
	}
	if len(graph.paths) != 1 {
		t.Fatalf("expected only eligibility request, got %v", graph.paths)
	}
}

func TestDiscoverMarksActiveGroupsAcrossPaginatedInstances(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-10 * time.Minute)
	future := now.Add(50 * time.Minute)
	laterFuture := now.Add(3 * time.Hour)

	graph := &pagingFakeGraph{
		eligibilityPages: []eligibilityResponse{{
			Value: []eligibilityScheduleInstance{
				{ID: "g1-member", GroupID: "group-1", AccessID: "member"},
				{ID: "g1-owner", GroupID: "group-1", AccessID: "owner"},
				{ID: "g2-owner", GroupID: "GROUP-2", AccessID: "owner"},
				{ID: "g3-expired", GroupID: "group-3", AccessID: "member"},
				{ID: "g4-upcoming", GroupID: "group-4", AccessID: "member"},
				{ID: "g5-empty-type", GroupID: "group-5", AccessID: "member"},
			},
		}},
		activePages: []assignmentInstanceResponse{
			{
				Value: []assignmentScheduleInstance{
					{GroupID: "GROUP-1", AccessID: "MEMBER", AssignmentType: "activated", StartDateTime: &past, EndDateTime: &future},
					{GroupID: "group-1", AccessID: "member", AssignmentType: "Activated", StartDateTime: &past, EndDateTime: &laterFuture},
					{GroupID: "group-3", AccessID: "member", AssignmentType: "activated", StartDateTime: &past, EndDateTime: &past},
					{GroupID: "group-4", AccessID: "member", AssignmentType: "activated", StartDateTime: &future, EndDateTime: &laterFuture},
					{GroupID: "group-5", AccessID: "member", AssignmentType: "", StartDateTime: &past, EndDateTime: &future},
				},
				NextLink: "https://graph.microsoft.com/v1.0/group-active-next",
			},
			{
				Value: []assignmentScheduleInstance{
					{GroupID: "group-2", AccessID: "OWNER", AssignmentType: "Assigned", StartDateTime: &past},
				},
			},
		},
	}

	got, err := NewProvider(graph).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("expected 6 assignments, got %d", len(got))
	}
	if !got[0].Active || got[0].ActiveUntil == nil || !got[0].ActiveUntil.Equal(laterFuture) {
		t.Fatalf("expected g1-member active until %v, got %#v", laterFuture, got[0])
	}
	if got[1].Active || got[1].ActiveUntil != nil {
		t.Fatalf("expected g1-owner inactive, got %#v", got[1])
	}
	if !got[2].Active || got[2].ActiveUntil != nil {
		t.Fatalf("expected g2-owner active with nil ActiveUntil, got %#v", got[2])
	}
	for _, inactiveIdx := range []int{3, 4, 5} {
		if got[inactiveIdx].Active || got[inactiveIdx].ActiveUntil != nil {
			t.Fatalf("expected assignment %q inactive, got %#v", got[inactiveIdx].ID, got[inactiveIdx])
		}
	}
	if len(graph.paths) != 3 || graph.paths[2] != "https://graph.microsoft.com/v1.0/group-active-next" {
		t.Fatalf("expected paginated active group requests, got %v", graph.paths)
	}
}

func TestDiscoverPropagatesActiveGroupLookupError(t *testing.T) {
	graph := &pagingFakeGraph{
		eligibilityPages: []eligibilityResponse{{
			Value: []eligibilityScheduleInstance{{ID: "g1-member", GroupID: "group-1", AccessID: "member"}},
		}},
		activeErr: errors.New("forbidden"),
	}

	_, err := NewProvider(graph).Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "discover active PIM groups:") || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected wrapped active group lookup error, got %v", err)
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

	if body.Action != "selfActivate" || body.AccessID != "owner" || body.ScheduleInfo.Expiration.Type != "AfterDuration" || body.ScheduleInfo.Expiration.Duration != "PT1H" {
		t.Fatalf("unexpected body: %#v", body)
	}
}

type fakeGraph struct {
	paths    []string
	response eligibilityResponse
}

func (f *fakeGraph) Get(_ context.Context, path string, out any) error {
	f.paths = append(f.paths, path)
	switch target := out.(type) {
	case *eligibilityResponse:
		*target = f.response
	case *assignmentInstanceResponse:
		*target = assignmentInstanceResponse{}
	}
	return nil
}

func (f *fakeGraph) Post(context.Context, string, any, any) error { return nil }

type pagingFakeGraph struct {
	paths            []string
	eligibilityPages []eligibilityResponse
	eligibilityIdx   int
	activePages      []assignmentInstanceResponse
	activeIdx        int
	activeErr        error
}

func (f *pagingFakeGraph) Get(_ context.Context, path string, out any) error {
	f.paths = append(f.paths, path)
	switch target := out.(type) {
	case *eligibilityResponse:
		*target = f.eligibilityPages[f.eligibilityIdx]
		f.eligibilityIdx++
		return nil
	case *assignmentInstanceResponse:
		if f.activeErr != nil {
			return f.activeErr
		}
		*target = f.activePages[f.activeIdx]
		f.activeIdx++
		return nil
	default:
		return errors.New("unexpected response type")
	}
}

func (f *pagingFakeGraph) Post(context.Context, string, any, any) error { return nil }
func TestNormalizeUsesExpandedGroupNameAndScheduleID(t *testing.T) {
	var item eligibilityScheduleInstance
	if err := json.Unmarshal([]byte(`{"id":"instance-1","eligibilityScheduleId":"schedule-1","accessId":"owner","groupId":"g1","principalId":"u1","group":{"id":"g1","displayName":"Production Operators"}}`), &item); err != nil {
		t.Fatal(err)
	}
	got := normalizeEligibility(item)
	if got.Scope.DisplayName != "Production Operators" || !strings.Contains(got.DisplayName, "Production Operators") || !strings.Contains(got.DisplayName, "owner") || got.EligibilityScheduleID != "schedule-1" {
		t.Fatalf("missing readable group/schedule metadata: %#v", got)
	}
}

func TestPrepareGroupPolicyAndActivationFailurePreserveTargetAndCheckedToken(t *testing.T) {
	posts := 0
	client := graph.NewClient(&http.Client{Transport: groupRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			if !strings.Contains(req.URL.Query().Get("$filter"), "scopeType eq 'Group' and roleDefinitionId eq 'owner'") {
				t.Fatalf("wrong policy target: %s", req.URL)
			}
			return groupResponse(200, `{"value":[{"scopeId":"g1","scopeType":"Group","roleDefinitionId":"owner","policy":{"effectiveRules":[{"id":"Expiration_EndUser_Assignment","maximumDuration":"PT25M","target":{"caller":"EndUser","level":"Assignment","operations":["All"]}},{"id":"Enablement_EndUser_Assignment","enabledRules":["Justification","MultiFactorAuthentication"],"target":{"caller":"EndUser","level":"Assignment","operations":["All"]}},{"id":"AuthenticationContext_EndUser_Assignment","isEnabled":true,"claimValue":"c4","target":{"caller":"EndUser","level":"Assignment","operations":["All"]}}]}}]}`), nil
		}
		posts++
		if req.URL.Path != "/v1.0/identityGovernance/privilegedAccess/group/assignmentScheduleRequests" || req.Header.Get("Authorization") != "Bearer checked" {
			t.Fatalf("wrong activation endpoint/token: %s %s", req.URL, req.Header.Get("Authorization"))
		}
		var body activationRequestBody
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Action != "selfActivate" || body.AccessID != "owner" || body.GroupID != "g1" || body.PrincipalID != "u1" || body.ScheduleInfo.Expiration.Duration != "PT17M" {
			t.Fatalf("wrong self activation target: %#v", body)
		}
		return groupResponse(403, `{"error":{"code":"Forbidden","message":"AuthenticationContext c4 is required"}}`), nil
	})}, groupTokenSource{})
	provider := NewProvider(client)
	input := []pim.EligibleAssignment{{ID: "one", PrincipalID: "u1", GroupID: "g1", AccessID: "owner"}}
	prepared, err := provider.Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if input[0].ActivationPolicy.MaximumDurationISO != "" || prepared[0].ActivationPolicy.MaximumDurationISO != "PT25M" || !prepared[0].ActivationPolicy.JustificationRequired || !prepared[0].ActivationPolicy.MFARequired || prepared[0].ActivationPolicy.AuthenticationContext != "c4" {
		t.Fatalf("wrong group policy: %#v", prepared)
	}
	_, err = provider.Activate(graph.WithAccessToken(context.Background(), "checked"), pim.ActivationRequest{Assignment: prepared[0], Justification: "Incident", DurationISO: "PT17M"})
	var responseErr graph.ResponseError
	if !errors.As(err, &responseErr) || activation.IsRetryable(err) || !strings.Contains(err.Error(), "AuthenticationContext c4") || posts != 1 {
		t.Fatalf("lost actionable claims failure or resubmitted: posts=%d err=%v", posts, err)
	}
}

func TestGroupActivationMapsProvisioningAndRetryableErrors(t *testing.T) {
	for _, test := range []struct {
		code   int
		body   string
		status pim.ActivationStatus
		retry  bool
	}{
		{201, `{"status":"PendingApproval"}`, pim.ActivationStatusPendingApproval, false},
		{201, `{"status":"PendingProvisioning"}`, pim.ActivationStatusPendingProvisioning, false},
		{201, `{"status":"ScheduleCreated"}`, pim.ActivationStatusActivated, false},
		{201, `{"status":"Denied"}`, pim.ActivationStatusFailed, false},
		{429, `{"error":{"message":"throttled"}}`, "", true},
	} {
		calls := 0
		client := graph.NewClient(&http.Client{Transport: groupRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return groupResponse(test.code, test.body), nil })}, groupTokenSource{})
		result, err := NewProvider(client).Activate(context.Background(), pim.ActivationRequest{})
		if result.Status != test.status || activation.IsRetryable(err) != test.retry || calls != 1 {
			t.Fatalf("wrong activation outcome: %#v err=%v calls=%d", result, err, calls)
		}
	}
}

type groupRoundTrip func(*http.Request) (*http.Response, error)

func (f groupRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type groupTokenSource struct{}

func (groupTokenSource) AccessToken(context.Context, string) (string, error) {
	return "discovery-token", nil
}
func groupResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestNormalizeKeepsGroupIDWhenDisplayNameIsUnavailable(t *testing.T) {
	got := normalizeEligibility(eligibilityScheduleInstance{ID: "instance", GroupID: "group-guid", AccessID: "member"})
	if got.Scope.DisplayName != "group-guid" || !strings.Contains(got.DisplayName, "group-guid") || !strings.Contains(got.DisplayName, "member") {
		t.Fatalf("unreadable group without directory permissions: %#v", got)
	}
}
