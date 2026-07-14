package groups

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/mathwro/pim-manager/internal/pim"
)

type GraphClient interface {
	Get(context.Context, string, any) error
	Post(context.Context, string, any, any) error
}

type Provider struct {
	graph       GraphClient
	principalID string
}

func NewProvider(graph GraphClient, principalID string) Provider {
	return Provider{graph: graph, principalID: principalID}
}

type eligibilityResponse struct {
	Value []eligibilityScheduleInstance `json:"value"`
}

type eligibilityScheduleInstance struct {
	ID          string `json:"id"`
	AccessID    string `json:"accessId"`
	PrincipalID string `json:"principalId"`
	GroupID     string `json:"groupId"`
}

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	var response eligibilityResponse
	principalID := strings.ReplaceAll(p.principalID, "'", "''")
	filter := url.QueryEscape("principalId eq '" + principalID + "'")
	path := "/identityGovernance/privilegedAccess/group/eligibilityScheduleInstances?$filter=" + filter
	if err := p.graph.Get(ctx, path, &response); err != nil {
		return nil, err
	}
	assignments := make([]pim.EligibleAssignment, 0, len(response.Value))
	for _, item := range response.Value {
		assignments = append(assignments, normalizeEligibility(item))
	}
	return assignments, nil
}

func normalizeEligibility(item eligibilityScheduleInstance) pim.EligibleAssignment {
	kind := pim.AssignmentKindGroupMember
	if item.AccessID == "owner" {
		kind = pim.AssignmentKindGroupOwner
	}
	return pim.EligibleAssignment{
		ID:                    item.ID,
		Source:                pim.AssignmentSourceGroup,
		Kind:                  kind,
		DisplayName:           "Group " + item.AccessID,
		PrincipalID:           item.PrincipalID,
		GroupID:               item.GroupID,
		AccessID:              item.AccessID,
		EligibilityScheduleID: item.ID,
		Scope: pim.Scope{
			ID:          item.GroupID,
			DisplayName: item.GroupID,
			Type:        pim.ScopeTypeGroup,
		},
	}
}

type activationRequestBody struct {
	AccessID      string       `json:"accessId"`
	PrincipalID   string       `json:"principalId"`
	GroupID       string       `json:"groupId"`
	Action        string       `json:"action"`
	ScheduleInfo  scheduleInfo `json:"scheduleInfo"`
	Justification string       `json:"justification"`
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
	err := p.graph.Post(ctx, "/identityGovernance/privilegedAccess/group/assignmentScheduleRequests", activationBody(request), &response)
	if err != nil {
		return pim.ActivationResult{}, err
	}
	switch response.Status {
	case "Granted", "Provisioned":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusActivated, Message: response.Status}, nil
	case "PendingApproval", "PendingApprovalProvisioning", "PendingAdminDecision":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusPendingApproval, Message: response.Status}, nil
	default:
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusFailed, Message: response.Status}, nil
	}
}

func activationBody(request pim.ActivationRequest) activationRequestBody {
	return activationRequestBody{
		AccessID:      request.Assignment.AccessID,
		PrincipalID:   request.Assignment.PrincipalID,
		GroupID:       request.Assignment.GroupID,
		Action:        "selfActivate",
		Justification: request.Justification,
		ScheduleInfo: scheduleInfo{
			StartDateTime: time.Now().UTC().Format(time.RFC3339),
			Expiration: expiration{
				Type:     "afterDuration",
				Duration: request.DurationISO,
			},
		},
	}
}
