package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/graph"
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

type eligibilityResponse struct {
	Value    []eligibilityScheduleInstance `json:"value"`
	NextLink string                        `json:"@odata.nextLink"`
}

type eligibilityScheduleInstance struct {
	ID                    string `json:"id"`
	EligibilityScheduleID string `json:"eligibilityScheduleId"`
	AccessID              string `json:"accessId"`
	PrincipalID           string `json:"principalId"`
	GroupID               string `json:"groupId"`
	Group                 struct {
		DisplayName string `json:"displayName"`
	} `json:"group"`
}

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	path := "/identityGovernance/privilegedAccess/group/eligibilityScheduleInstances/filterByCurrentUser(on='principal')?$expand=group"
	assignments := make([]pim.EligibleAssignment, 0)
	for path != "" {
		var response eligibilityResponse
		if err := p.graph.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("discover eligible PIM groups: %w", err)
		}
		for _, item := range response.Value {
			assignments = append(assignments, normalizeEligibility(item))
		}
		path = response.NextLink
	}
	return assignments, nil
}

func (p Provider) Prepare(ctx context.Context, assignments []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	return graph.PrepareAssignments(ctx, p.graph, assignments)
}

func normalizeEligibility(item eligibilityScheduleInstance) pim.EligibleAssignment {
	kind := pim.AssignmentKindGroupMember
	if item.AccessID == "owner" {
		kind = pim.AssignmentKindGroupOwner
	}
	groupName := item.Group.DisplayName
	if groupName == "" {
		groupName = item.GroupID
	}
	return pim.EligibleAssignment{
		ID:                    item.ID,
		Source:                pim.AssignmentSourceGroup,
		Kind:                  kind,
		DisplayName:           groupName + " (" + item.AccessID + ")",
		PrincipalID:           item.PrincipalID,
		GroupID:               item.GroupID,
		AccessID:              item.AccessID,
		EligibilityScheduleID: item.EligibilityScheduleID,
		Scope: pim.Scope{
			ID:          item.GroupID,
			DisplayName: groupName,
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
		return pim.ActivationResult{}, activation.WrapRetryable(fmt.Errorf("activate PIM group %s: %w", request.Assignment.DisplayName, err))
	}
	return graph.ActivationResult(request.Assignment, response.Status), nil
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
