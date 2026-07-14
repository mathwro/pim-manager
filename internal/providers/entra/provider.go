package entra

import (
	"context"
	"time"

	"github.com/mathwro/pim-manager/internal/pim"
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

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	path := "/roleManagement/directory/roleEligibilitySchedules/filterByCurrentUser(on='principal')?$expand=roleDefinition"
	assignments := make([]pim.EligibleAssignment, 0)
	for path != "" {
		var response roleEligibilityResponse
		if err := p.graph.Get(ctx, path, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Value {
			assignments = append(assignments, normalizeEligibility(item))
		}
		path = response.NextLink
	}
	return assignments, nil
}

func normalizeEligibility(item roleEligibilitySchedule) pim.EligibleAssignment {
	scopeType := pim.ScopeTypeTenant
	scopeName := "Tenant"
	if item.DirectoryScopeID != "/" && item.DirectoryScopeID != "" {
		scopeName = item.DirectoryScopeID
	}
	return pim.EligibleAssignment{
		ID:                    item.ID,
		Source:                pim.AssignmentSourceEntra,
		Kind:                  pim.AssignmentKindDirectoryRole,
		DisplayName:           item.RoleDefinition.DisplayName,
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
	Action           string       `json:"action"`
	PrincipalID      string       `json:"principalId"`
	RoleDefinitionID string       `json:"roleDefinitionId"`
	DirectoryScopeID string       `json:"directoryScopeId,omitempty"`
	AppScopeID       string       `json:"appScopeId,omitempty"`
	Justification    string       `json:"justification"`
	ScheduleInfo     scheduleInfo `json:"scheduleInfo"`
}

type scheduleInfo struct {
	StartDateTime string     `json:"startDateTime"`
	Expiration    expiration `json:"expiration"`
}

type expiration struct {
	Type     string `json:"type"`
	Duration string `json:"duration"`
}

type activationResponse struct {
	Status string `json:"status"`
}

func (p Provider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	var response activationResponse
	err := p.graph.Post(ctx, "/roleManagement/directory/roleAssignmentScheduleRequests", activationBody(request), &response)
	if err != nil {
		return pim.ActivationResult{}, err
	}
	return p.mapStatus(request.Assignment, response.Status, ""), nil
}

func activationBody(request pim.ActivationRequest) activationRequestBody {
	return activationRequestBody{
		Action:           "selfActivate",
		PrincipalID:      request.Assignment.PrincipalID,
		RoleDefinitionID: request.Assignment.RoleDefinitionID,
		DirectoryScopeID: request.Assignment.DirectoryScopeID,
		AppScopeID:       request.Assignment.AppScopeID,
		Justification:    request.Justification,
		ScheduleInfo: scheduleInfo{
			StartDateTime: time.Now().UTC().Format(time.RFC3339),
			Expiration: expiration{
				Type:     "AfterDuration",
				Duration: request.DurationISO,
			},
		},
	}
}

func (p Provider) mapStatus(assignment pim.EligibleAssignment, status string, message string) pim.ActivationResult {
	switch status {
	case "Granted", "Provisioned":
		return pim.ActivationResult{Assignment: assignment, Status: pim.ActivationStatusActivated, Message: status}
	case "PendingApproval", "PendingApprovalProvisioning", "PendingAdminDecision":
		return pim.ActivationResult{Assignment: assignment, Status: pim.ActivationStatusPendingApproval, Message: status}
	default:
		return pim.ActivationResult{Assignment: assignment, Status: pim.ActivationStatusFailed, Message: status + " " + message, Retryable: false}
	}
}
