package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestPrepareAssignmentsLoadsTargetedPoliciesAndClonesInput(t *testing.T) {
	input := []pim.EligibleAssignment{
		{ID: "directory", Source: pim.AssignmentSourceEntra, RoleDefinitionID: "role-1", DirectoryScopeID: "/administrativeUnits/unit-1"},
		{ID: "same-role", Source: pim.AssignmentSourceEntra, RoleDefinitionID: "role-1"},
		{ID: "member", Source: pim.AssignmentSourceGroup, GroupID: "group-1", AccessID: "member"},
		{ID: "owner", Source: pim.AssignmentSourceGroup, GroupID: "group-1", AccessID: "owner"},
	}
	client := &policyFake{get: func(path string) (string, error) {
		u, err := url.Parse(path)
		if err != nil {
			return "", err
		}
		filter := u.Query().Get("$filter")
		if u.Path != "/policies/roleManagementPolicyAssignments" || u.Query().Get("$expand") != "policy($expand=effectiveRules)" {
			return "", fmt.Errorf("unexpected policy query %s", path)
		}
		switch filter {
		case "scopeId eq '/' and scopeType eq 'DirectoryRole' and roleDefinitionId eq 'role-1'":
			return policyFixture("/", "DirectoryRole", "role-1", "PT1H45M", `"Justification","MultiFactorAuthentication"`, true, "c3"), nil
		case "scopeId eq 'group-1' and scopeType eq 'Group' and roleDefinitionId eq 'member'":
			return policyFixture("group-1", "Group", "member", "PT4H", ``, false, "stale-c1"), nil
		case "scopeId eq 'group-1' and scopeType eq 'Group' and roleDefinitionId eq 'owner'":
			return policyFixture("group-1", "Group", "owner", "PT30M", `"Justification"`, true, "c5"), nil
		default:
			return "", fmt.Errorf("wrong policy targeting: %s", filter)
		}
	}}
	prepared, err := PrepareAssignments(context.Background(), client, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.paths) != 3 {
		t.Fatalf("expected one lookup per distinct policy target, got %v", client.paths)
	}
	want := []pim.ActivationPolicy{
		{MaximumDurationISO: "PT1H45M", JustificationRequired: true, MFARequired: true, AuthenticationContext: "c3"},
		{MaximumDurationISO: "PT1H45M", JustificationRequired: true, MFARequired: true, AuthenticationContext: "c3"},
		{MaximumDurationISO: "PT4H"},
		{MaximumDurationISO: "PT30M", JustificationRequired: true, AuthenticationContext: "c5"},
	}
	for index := range prepared {
		if prepared[index].ActivationPolicy != want[index] {
			t.Fatalf("policy %d: got %#v want %#v", index, prepared[index].ActivationPolicy, want[index])
		}
		if input[index].ActivationPolicy != (pim.ActivationPolicy{}) {
			t.Fatal("Prepare mutated discovery/cache input")
		}
	}
}

func TestPrepareAssignmentsFailsClosedOnUnresolvablePolicies(t *testing.T) {
	good := policyFixture("/", "DirectoryRole", "role-1", "PT8H", ``, false, "")
	for _, test := range []struct{ name, response string }{
		{"missing assignment", `{"value":[]}`},
		{"wrong role", strings.ReplaceAll(good, `"role-1"`, `"other-role"`)},
		{"wrong scope", strings.ReplaceAll(good, `"scopeId":"/"`, `"scopeId":"other"`)},
		{"missing duration", strings.ReplaceAll(good, `"maximumDuration":"PT8H"`, `"maximumDuration":""`)},
		{"admin duration only", strings.ReplaceAll(good, `"Expiration_EndUser_Assignment"`, `"Expiration_Admin_Assignment"`)},
		{"wrong caller", strings.ReplaceAll(good, `"caller":"EndUser"`, `"caller":"Admin"`)},
		{"wrong level", strings.ReplaceAll(good, `"level":"Assignment"`, `"level":"Eligibility"`)},
		{"wrong operation", strings.ReplaceAll(good, `"operations":["All"]`, `"operations":["Deactivate"]`)},
		{"missing enablement", strings.ReplaceAll(good, `"Enablement_EndUser_Assignment"`, `"Enablement_Admin_Assignment"`)},
		{"unknown enablement", strings.ReplaceAll(good, `"enabledRules":[]`, `"enabledRules":["Ticketing"]`)},
		{"missing context state", strings.ReplaceAll(good, `"isEnabled":false,`, ``)},
		{"empty enabled context", strings.ReplaceAll(good, `"isEnabled":false`, `"isEnabled":true`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &policyFake{get: func(string) (string, error) { return test.response, nil }}
			input := []pim.EligibleAssignment{{ID: "eligibility", DisplayName: "Reader", Source: pim.AssignmentSourceEntra, RoleDefinitionID: "role-1"}}
			prepared, err := PrepareAssignments(context.Background(), client, input)
			if err == nil || prepared != nil {
				t.Fatalf("unresolved policy allowed activation: %#v, %v", prepared, err)
			}
			if !strings.Contains(err.Error(), "Reader") {
				t.Fatalf("policy error does not identify assignment: %v", err)
			}
		})
	}
}

func TestPrepareAssignmentsFollowsAssignmentAndRulePages(t *testing.T) {
	fixture := policyFixture("/", "DirectoryRole", "role-1", "PT2H", ``, false, "")
	var expanded struct {
		Value []struct {
			Policy struct {
				EffectiveRules json.RawMessage `json:"effectiveRules"`
			} `json:"policy"`
		} `json:"value"`
	}
	if err := json.Unmarshal([]byte(fixture), &expanded); err != nil {
		t.Fatal(err)
	}
	client := &policyFake{get: func(path string) (string, error) {
		switch {
		case strings.HasPrefix(path, "/policies/roleManagementPolicyAssignments?"):
			return `{"value":[],"@odata.nextLink":"https://graph.microsoft.com/v1.0/assignment-page"}`, nil
		case path == "https://graph.microsoft.com/v1.0/assignment-page":
			return `{"value":[{"scopeId":"/","scopeType":"DirectoryRole","roleDefinitionId":"role-1","policyId":"policy-1"}]}`, nil
		case path == "/policies/roleManagementPolicies/policy-1/effectiveRules":
			return `{"value":[],"@odata.nextLink":"https://graph.microsoft.com/v1.0/rule-page"}`, nil
		case path == "https://graph.microsoft.com/v1.0/rule-page":
			return `{"value":` + string(expanded.Value[0].Policy.EffectiveRules) + `}`, nil
		default:
			return "", fmt.Errorf("unexpected request %s", path)
		}
	}}
	prepared, err := PrepareAssignments(context.Background(), client, []pim.EligibleAssignment{{Source: pim.AssignmentSourceEntra, RoleDefinitionID: "role-1"}})
	if err != nil || len(prepared) != 1 || prepared[0].ActivationPolicy.MaximumDurationISO != "PT2H" || len(client.paths) != 4 {
		t.Fatalf("policy pagination failed: %#v, paths=%v err=%v", prepared, client.paths, err)
	}
}

func TestPrepareAssignmentsPreservesWrappedLookupFailure(t *testing.T) {
	denied := ResponseError{StatusCode: 403, Status: "403 Forbidden", Body: "admin consent required"}
	client := &policyFake{get: func(string) (string, error) { return "", denied }}
	_, err := PrepareAssignments(context.Background(), client, []pim.EligibleAssignment{{Source: pim.AssignmentSourceGroup, GroupID: "g", AccessID: "owner"}})
	var responseErr ResponseError
	if !errors.As(err, &responseErr) || !strings.Contains(err.Error(), "admin consent required") {
		t.Fatalf("lost actionable Graph failure: %v", err)
	}
}

func policyFixture(scope, scopeType, role, duration, enabled string, contextEnabled bool, claim string) string {
	return fmt.Sprintf(`{"value":[{"scopeId":%q,"scopeType":%q,"roleDefinitionId":%q,"policyId":"policy-1","policy":{"id":"policy-1","effectiveRules":[
		{"@odata.type":"#microsoft.graph.unifiedRoleManagementPolicyExpirationRule","id":"Expiration_EndUser_Assignment","maximumDuration":%q,"isExpirationRequired":true,"target":{"caller":"EndUser","level":"Assignment","operations":["All"]}},
		{"@odata.type":"#microsoft.graph.unifiedRoleManagementPolicyEnablementRule","id":"Enablement_EndUser_Assignment","enabledRules":[%s],"target":{"caller":"EndUser","level":"Assignment","operations":["All"]}},
		{"@odata.type":"#microsoft.graph.unifiedRoleManagementPolicyAuthenticationContextRule","id":"AuthenticationContext_EndUser_Assignment","isEnabled":%t,"claimValue":%q,"target":{"caller":"EndUser","level":"Assignment","operations":["All"]}}
	]}}]}`, scope, scopeType, role, duration, enabled, contextEnabled, claim)
}

type policyFake struct {
	paths []string
	get   func(string) (string, error)
}

func (f *policyFake) Get(_ context.Context, path string, out any) error {
	f.paths = append(f.paths, path)
	body, err := f.get(path)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(body), out)
}

func TestPrepareAssignmentsRejectsAmbiguousPolicies(t *testing.T) {
	fixture := policyFixture("/", "DirectoryRole", "role-1", "PT8H", ``, false, "")
	var response map[string]any
	if err := json.Unmarshal([]byte(fixture), &response); err != nil {
		t.Fatal(err)
	}
	values := response["value"].([]any)
	response["value"] = append(values, values[0])
	duplicate, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	client := &policyFake{get: func(string) (string, error) { return string(duplicate), nil }}
	_, err = PrepareAssignments(context.Background(), client, []pim.EligibleAssignment{{Source: pim.AssignmentSourceEntra, RoleDefinitionID: "role-1"}})
	if err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("ambiguous policy was accepted: %v", err)
	}
}

func TestPrepareAssignmentsRejectsMissingAuthenticationContextRule(t *testing.T) {
	fixture := policyFixture("/", "DirectoryRole", "role-1", "PT8H", ``, false, "")
	fixture = strings.ReplaceAll(fixture, `"AuthenticationContext_EndUser_Assignment"`, `"AuthenticationContext_Admin_Assignment"`)
	client := &policyFake{get: func(string) (string, error) { return fixture, nil }}
	_, err := PrepareAssignments(context.Background(), client, []pim.EligibleAssignment{{Source: pim.AssignmentSourceEntra, RoleDefinitionID: "role-1"}})
	if err == nil {
		t.Fatal("missing authentication context metadata silently treated as disabled")
	}
}

func TestPrepareAssignmentsAcceptsActivateTargetAndIgnoresAdminRules(t *testing.T) {
	fixture := policyFixture("/", "DirectoryRole", "role-1", "PT8H", ``, false, "stale-c1")
	fixture = strings.ReplaceAll(fixture, `"operations":["All"]`, `"operations":["Activate"]`)
	var response map[string]any
	if err := json.Unmarshal([]byte(fixture), &response); err != nil {
		t.Fatal(err)
	}
	policy := response["value"].([]any)[0].(map[string]any)["policy"].(map[string]any)
	policy["effectiveRules"] = append(policy["effectiveRules"].([]any), map[string]any{"id": "Expiration_EndUser_Assignment", "maximumDuration": "P365D", "target": map[string]any{"caller": "Admin", "level": "Assignment", "operations": []string{"All"}}})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	client := &policyFake{get: func(string) (string, error) { return string(encoded), nil }}
	prepared, err := PrepareAssignments(context.Background(), client, []pim.EligibleAssignment{{Source: pim.AssignmentSourceEntra, RoleDefinitionID: "role-1"}})
	if err != nil || len(prepared) != 1 || prepared[0].ActivationPolicy.MaximumDurationISO != "PT8H" || prepared[0].ActivationPolicy.AuthenticationContext != "" {
		t.Fatalf("wrong end-user targeting: %#v %v", prepared, err)
	}
}

func TestPrepareAssignmentsUsesInheritedEffectiveRules(t *testing.T) {
	var local, inherited map[string]any
	if err := json.Unmarshal([]byte(policyFixture("/", "DirectoryRole", "role-1", "PT8H", ``, false, "")), &local); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(policyFixture("/", "DirectoryRole", "role-1", "PT30M", `"Justification","MultiFactorAuthentication"`, true, "c9")), &inherited); err != nil {
		t.Fatal(err)
	}
	policy := local["value"].([]any)[0].(map[string]any)["policy"].(map[string]any)
	policy["rules"] = policy["effectiveRules"]
	policy["effectiveRules"] = inherited["value"].([]any)[0].(map[string]any)["policy"].(map[string]any)["effectiveRules"]
	encoded, err := json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	client := &policyFake{get: func(string) (string, error) { return string(encoded), nil }}
	prepared, err := PrepareAssignments(context.Background(), client, []pim.EligibleAssignment{{Source: pim.AssignmentSourceEntra, RoleDefinitionID: "role-1"}})
	want := pim.ActivationPolicy{MaximumDurationISO: "PT30M", JustificationRequired: true, MFARequired: true, AuthenticationContext: "c9"}
	if err != nil || len(prepared) != 1 || prepared[0].ActivationPolicy != want {
		t.Fatalf("inherited requirements ignored: %#v %v", prepared, err)
	}
}
