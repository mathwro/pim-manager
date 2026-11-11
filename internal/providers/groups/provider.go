package groups

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

type assignmentInstanceResponse struct {
	Value    []assignmentScheduleInstance `json:"value"`
	NextLink string                       `json:"@odata.nextLink"`
}

type assignmentScheduleInstance struct {
	GroupID        string     `json:"groupId"`
	AccessID       string     `json:"accessId"`
	AssignmentType string     `json:"assignmentType"`
	StartDateTime  *time.Time `json:"startDateTime"`
	EndDateTime    *time.Time `json:"endDateTime"`
}

type groupAssignmentKey struct {
	groupID  string
	accessID string
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
	if len(assignments) == 0 {
		return assignments, nil
	}
	active, err := p.discoverActiveAssignments(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover active PIM groups: %w", err)
	}
	applyActiveState(assignments, active, time.Now().UTC())
	return assignments, nil
}

func (p Provider) discoverActiveAssignments(ctx context.Context) ([]assignmentScheduleInstance, error) {
	path := "/identityGovernance/privilegedAccess/group/assignmentScheduleInstances/filterByCurrentUser(on='principal')"
	var instances []assignmentScheduleInstance
	for path != "" {
		var response assignmentInstanceResponse
		if err := p.graph.Get(ctx, path, &response); err != nil {
			return nil, err
		}
		instances = append(instances, response.Value...)
		path = response.NextLink
	}
	return instances, nil
}

func applyActiveState(assignments []pim.EligibleAssignment, instances []assignmentScheduleInstance, now time.Time) {
	byKey := make(map[groupAssignmentKey]assignmentScheduleInstance, len(instances))
	for _, instance := range instances {
		if !isCurrentInstance(instance, now) {
			continue
		}
		key := makeGroupKey(instance.GroupID, instance.AccessID)
		if key.groupID == "" || key.accessID == "" {
			continue
		}
		if existing, ok := byKey[key]; !ok || preferInstanceEnd(existing.EndDateTime, instance.EndDateTime) {
			byKey[key] = instance
		}
	}
	for index := range assignments {
		key := makeGroupKey(assignments[index].GroupID, assignments[index].AccessID)
		instance, ok := byKey[key]
		if !ok {
			continue
		}
		assignments[index].Active = true
		assignments[index].ActiveUntil = instance.EndDateTime
	}
}

func isCurrentInstance(instance assignmentScheduleInstance, now time.Time) bool {
	if !strings.EqualFold(instance.AssignmentType, "activated") && !strings.EqualFold(instance.AssignmentType, "assigned") {
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

func makeGroupKey(groupID, accessID string) groupAssignmentKey {
	return groupAssignmentKey{
		groupID:  strings.ToLower(strings.TrimSpace(groupID)),
		accessID: strings.ToLower(strings.TrimSpace(accessID)),
	}
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
	AccessID      string             `json:"accessId"`
	PrincipalID   string             `json:"principalId"`
	GroupID       string             `json:"groupId"`
	Action        string             `json:"action"`
	ScheduleInfo  graph.ScheduleInfo `json:"scheduleInfo"`
	Justification string             `json:"justification"`
}

func (p Provider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	var response graph.ActivationResponse
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
		ScheduleInfo:  graph.AfterDurationSchedule(time.Now(), request.DurationISO),
	}
}
