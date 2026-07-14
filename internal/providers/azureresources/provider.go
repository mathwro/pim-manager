package azureresources

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/pim"
)

type Provider struct {
	arm         ARMClient
	principalID string
	scopes      []string
}

func NewProvider(arm ARMClient, principalID string, scopes []string) Provider {
	return Provider{arm: arm, principalID: principalID, scopes: scopes}
}

type eligibilityResponse struct {
	Value    []roleEligibilityScheduleInstance `json:"value"`
	NextLink string                            `json:"nextLink"`
}

type roleEligibilityScheduleInstance struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name"`
	Properties roleEligibilityProperties `json:"properties"`
}

type roleEligibilityProperties struct {
	Scope                     string             `json:"scope"`
	RoleDefinitionID          string             `json:"roleDefinitionId"`
	PrincipalID               string             `json:"principalId"`
	RoleEligibilityScheduleID string             `json:"roleEligibilityScheduleId"`
	Condition                 string             `json:"condition"`
	ConditionVersion          string             `json:"conditionVersion"`
	ExpandedProperties        expandedProperties `json:"expandedProperties"`
}

type expandedProperties struct {
	Scope          expandedScope          `json:"scope"`
	RoleDefinition expandedRoleDefinition `json:"roleDefinition"`
}

type expandedScope struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

type expandedRoleDefinition struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	var assignments []pim.EligibleAssignment
	for _, scope := range p.scopes {
		filter := url.QueryEscape(fmt.Sprintf("assignedTo('%s')", p.principalID))
		path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=%s&api-version=%s", scope, filter, arm.AuthorizationAPIVersion)
		for path != "" {
			var response eligibilityResponse
			if err := p.arm.Get(ctx, path, &response); err != nil {
				return nil, err
			}
			for _, item := range response.Value {
				assignments = append(assignments, normalizeEligibility(item))
			}
			path = response.NextLink
		}
	}
	return assignments, nil
}

func normalizeEligibility(item roleEligibilityScheduleInstance) pim.EligibleAssignment {
	scopeType := mapScopeType(item.Properties.ExpandedProperties.Scope.Type, item.Properties.Scope)
	return pim.EligibleAssignment{
		ID:                    item.ID,
		Source:                pim.AssignmentSourceAzureResource,
		Kind:                  pim.AssignmentKindAzureRole,
		DisplayName:           item.Properties.ExpandedProperties.RoleDefinition.DisplayName,
		PrincipalID:           item.Properties.PrincipalID,
		RoleDefinitionID:      item.Properties.RoleDefinitionID,
		AzureScope:            item.Properties.Scope,
		EligibilityScheduleID: item.Properties.RoleEligibilityScheduleID,
		Condition:             item.Properties.Condition,
		ConditionVersion:      item.Properties.ConditionVersion,
		Scope: pim.Scope{
			ID:          item.Properties.Scope,
			DisplayName: item.Properties.ExpandedProperties.Scope.DisplayName,
			Type:        scopeType,
		},
	}
}

func mapScopeType(apiType string, scope string) pim.ScopeType {
	switch strings.ToLower(apiType) {
	case "managementgroup", "management group":
		return pim.ScopeTypeManagementGroup
	case "subscription":
		return pim.ScopeTypeSubscription
	case "resourcegroup", "resource group":
		return pim.ScopeTypeResourceGroup
	}
	if strings.Contains(strings.ToLower(scope), "/resourcegroups/") {
		return pim.ScopeTypeResourceGroup
	}
	if strings.Contains(strings.ToLower(scope), "/subscriptions/") {
		return pim.ScopeTypeSubscription
	}
	return pim.ScopeTypeManagementGroup
}

type activationRequestBody struct {
	Properties activationProperties `json:"properties"`
}

type activationProperties struct {
	PrincipalID                     string       `json:"principalId"`
	RequestType                     string       `json:"requestType"`
	RoleDefinitionID                string       `json:"roleDefinitionId"`
	LinkedRoleEligibilityScheduleID string       `json:"linkedRoleEligibilityScheduleId"`
	Justification                   string       `json:"justification"`
	Condition                       string       `json:"condition,omitempty"`
	ConditionVersion                string       `json:"conditionVersion,omitempty"`
	ScheduleInfo                    scheduleInfo `json:"scheduleInfo"`
}

type scheduleInfo struct {
	StartDateTime string     `json:"startDateTime"`
	Expiration    expiration `json:"expiration"`
}

type expiration struct {
	Type        string  `json:"type"`
	EndDateTime *string `json:"endDateTime"`
	Duration    string  `json:"duration"`
}

type activationResponse struct {
	Properties struct {
		Status string `json:"status"`
	} `json:"properties"`
}

func (p Provider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	requestID := uuid.NewString()
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleAssignmentScheduleRequests/%s?api-version=%s", request.Assignment.AzureScope, requestID, arm.AuthorizationAPIVersion)
	var response activationResponse
	if err := p.arm.Put(ctx, path, activationBody(request), &response); err != nil {
		return pim.ActivationResult{}, err
	}
	switch response.Properties.Status {
	case "Granted", "Provisioned":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusActivated, Message: response.Properties.Status}, nil
	case "PendingApproval", "PendingApprovalProvisioning", "PendingAdminDecision":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusPendingApproval, Message: response.Properties.Status}, nil
	default:
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusFailed, Message: response.Properties.Status}, nil
	}
}

func activationBody(request pim.ActivationRequest) activationRequestBody {
	return activationRequestBody{
		Properties: activationProperties{
			PrincipalID:                     request.Assignment.PrincipalID,
			RequestType:                     "SelfActivate",
			RoleDefinitionID:                request.Assignment.RoleDefinitionID,
			LinkedRoleEligibilityScheduleID: request.Assignment.EligibilityScheduleID,
			Justification:                   request.Justification,
			Condition:                       request.Assignment.Condition,
			ConditionVersion:                request.Assignment.ConditionVersion,
			ScheduleInfo: scheduleInfo{
				StartDateTime: time.Now().UTC().Format(time.RFC3339),
				Expiration: expiration{
					Type:     "AfterDuration",
					Duration: request.DurationISO,
				},
			},
		},
	}
}
