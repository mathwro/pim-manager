package entra

import (
	"context"
	"fmt"
	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/graph"
	"github.com/mathwro/pim-manager/internal/pim"
	"strings"
	"time"
)

type GraphClient interface {
	Get(context.Context, string, any) error
	Post(context.Context, string, any, any) error
}

type Provider struct {
	graph GraphClient
}

func NewProvider(graph GraphClient) Provider {
	return Provider{graph: graph}
}

type roleEligibilityResponse struct {
	Value    []roleEligibilitySchedule `json:"value"`
	NextLink string                    `json:"@odata.nextLink"`
}

type roleEligibilitySchedule struct {
	ID               string         `json:"id"`
	PrincipalID      string         `json:"principalId"`
	RoleDefinitionID string         `json:"roleDefinitionId"`
	DirectoryScopeID string         `json:"directoryScopeId"`
	AppScopeID       string         `json:"appScopeId"`
	RoleDefinition   roleDefinition `json:"roleDefinition"`
}

type roleDefinition struct {
	DisplayName string `json:"displayName"`
}
type roleAssignmentInstanceResponse struct {
	Value    []roleAssignmentScheduleInstance `json:"value"`
	NextLink string                           `json:"@odata.nextLink"`
}

type roleAssignmentScheduleInstance struct {
	RoleDefinitionID string     `json:"roleDefinitionId"`
	DirectoryScopeID string     `json:"directoryScopeId"`
	AppScopeID       string     `json:"appScopeId"`
	AssignmentType   string     `json:"assignmentType"`
	StartDateTime    *time.Time `json:"startDateTime"`
	EndDateTime      *time.Time `json:"endDateTime"`
}

type entraAssignmentKey struct {
	roleDefinitionID string
	directoryScopeID string
	appScopeID       string
}

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	path := "/roleManagement/directory/roleEligibilitySchedules/filterByCurrentUser(on='principal')?$expand=roleDefinition"
	assignments := make([]pim.EligibleAssignment, 0)
	for path != "" {
		var response roleEligibilityResponse
		if err := p.graph.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("discover eligible Entra roles: %w", err)
		}
		for _, item := range response.Value {
			assignments = append(assignments, normalizeEligibility(item))
		}
		path = response.NextLink
	}
	if len(assignments) == 0 {
		return assignments, nil
	}
	active, err := p.discoverActiveAssignments(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover active Entra roles: %w", err)
	}
	applyActiveState(assignments, active, time.Now().UTC())
	return assignments, nil
}

func (p Provider) discoverActiveAssignments(ctx context.Context) ([]roleAssignmentScheduleInstance, error) {
	path := "/roleManagement/directory/roleAssignmentScheduleInstances/filterByCurrentUser(on='principal')"
	var instances []roleAssignmentScheduleInstance
	for path != "" {
		var response roleAssignmentInstanceResponse
		if err := p.graph.Get(ctx, path, &response); err != nil {
			return nil, err
		}
		instances = append(instances, response.Value...)
		path = response.NextLink
	}
	return instances, nil
}

func applyActiveState(assignments []pim.EligibleAssignment, instances []roleAssignmentScheduleInstance, now time.Time) {
	byKey := make(map[entraAssignmentKey]roleAssignmentScheduleInstance, len(instances))
	for _, instance := range instances {
		if !isCurrentInstance(instance, now) {
			continue
		}
		key := makeEntraKey(instance.RoleDefinitionID, instance.DirectoryScopeID, instance.AppScopeID)
		if key.roleDefinitionID == "" {
			continue
		}
		if existing, ok := byKey[key]; !ok || preferInstanceEnd(existing.EndDateTime, instance.EndDateTime) {
			byKey[key] = instance
		}
	}
	for index := range assignments {
		key := makeEntraKey(assignments[index].RoleDefinitionID, assignments[index].DirectoryScopeID, assignments[index].AppScopeID)
		instance, ok := byKey[key]
		if !ok {
			continue
		}
		assignments[index].Active = true
		assignments[index].ActiveUntil = instance.EndDateTime
	}
}

func isCurrentInstance(instance roleAssignmentScheduleInstance, now time.Time) bool {
	if !strings.EqualFold(instance.AssignmentType, "Activated") && !strings.EqualFold(instance.AssignmentType, "Assigned") {
		return false
	}
	if instance.StartDateTime != nil && instance.StartDateTime.After(now) {
		return false
	}
	return instance.EndDateTime == nil || instance.EndDateTime.After(now)
}

func preferInstanceEnd(existing, candidate *time.Time) bool {
	if existing == nil {
		return false
	}
	if candidate == nil {
		return true
	}
	return candidate.After(*existing)
}

func makeEntraKey(roleDefinitionID, directoryScopeID, appScopeID string) entraAssignmentKey {
	return entraAssignmentKey{
		roleDefinitionID: strings.ToLower(strings.TrimSpace(roleDefinitionID)),
		directoryScopeID: normalizeScope(directoryScopeID),
		appScopeID:       normalizeScope(appScopeID),
	}
}

func normalizeScope(scope string) string {
	scope = strings.TrimRight(strings.TrimSpace(scope), "/")
	if scope == "" {
		return ""
	}
	return strings.ToLower(scope)
}

func (p Provider) Prepare(ctx context.Context, assignments []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	return graph.PrepareAssignments(ctx, p.graph, assignments)
}

func normalizeEligibility(item roleEligibilitySchedule) pim.EligibleAssignment {
	scopeType := pim.ScopeTypeTenant
	scopeName := "Tenant"
	if item.DirectoryScopeID != "/" && item.DirectoryScopeID != "" {
		scopeName = item.DirectoryScopeID
	}
	displayName := item.RoleDefinition.DisplayName
	if displayName == "" {
		displayName = item.RoleDefinitionID
	}
	return pim.EligibleAssignment{
		ID:                    item.ID,
		Source:                pim.AssignmentSourceEntra,
		Kind:                  pim.AssignmentKindDirectoryRole,
		DisplayName:           displayName,
		PrincipalID:           item.PrincipalID,
		RoleDefinitionID:      item.RoleDefinitionID,
		DirectoryScopeID:      item.DirectoryScopeID,
		AppScopeID:            item.AppScopeID,
		EligibilityScheduleID: item.ID,
		Scope: pim.Scope{
			ID:          item.DirectoryScopeID,
			DisplayName: scopeName,
			Type:        scopeType,
		},
	}
}

type activationRequestBody struct {
	Action           string             `json:"action"`
	PrincipalID      string             `json:"principalId"`
	RoleDefinitionID string             `json:"roleDefinitionId"`
	DirectoryScopeID string             `json:"directoryScopeId,omitempty"`
	AppScopeID       string             `json:"appScopeId,omitempty"`
	Justification    string             `json:"justification"`
	ScheduleInfo     graph.ScheduleInfo `json:"scheduleInfo"`
}

func (p Provider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	var response graph.ActivationResponse
	err := p.graph.Post(ctx, "/roleManagement/directory/roleAssignmentScheduleRequests", activationBody(request), &response)
	if err != nil {
		return pim.ActivationResult{}, activation.WrapRetryable(fmt.Errorf("activate Entra role %s: %w", request.Assignment.DisplayName, err))
	}
	return graph.ActivationResult(request.Assignment, response.Status), nil
}

func activationBody(request pim.ActivationRequest) activationRequestBody {
	return activationRequestBody{
		Action:           "selfActivate",
		PrincipalID:      request.Assignment.PrincipalID,
		RoleDefinitionID: request.Assignment.RoleDefinitionID,
		DirectoryScopeID: request.Assignment.DirectoryScopeID,
		AppScopeID:       request.Assignment.AppScopeID,
		Justification:    request.Justification,
		ScheduleInfo:     graph.AfterDurationSchedule(time.Now(), request.DurationISO),
	}
}
