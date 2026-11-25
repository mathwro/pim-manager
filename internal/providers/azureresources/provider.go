package azureresources

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/pim"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type ARMClient interface {
	PinAccessToken(context.Context) (context.Context, error)
	Get(context.Context, string, any) error
	Post(context.Context, string, any, any) error
	Put(context.Context, string, any, any) error
}
type Provider struct {
	arm ARMClient
}

func NewProvider(arm ARMClient) Provider {
	return Provider{arm: arm}
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
type childResourceResponse struct {
	Value    []eligibleChildResource `json:"value"`
	NextLink string                  `json:"nextLink"`
}

type eligibleChildResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

const resourceGraphAPIVersion = "2022-10-01"

type resourceGraphRequest struct {
	Query            string               `json:"query"`
	ManagementGroups []string             `json:"managementGroups,omitempty"`
	Subscriptions    []string             `json:"subscriptions,omitempty"`
	Options          resourceGraphOptions `json:"options"`
}

type resourceGraphOptions struct {
	ResultFormat       string `json:"resultFormat"`
	Top                int    `json:"$top"`
	SkipToken          string `json:"$skipToken,omitempty"`
	AllowPartialScopes bool   `json:"allowPartialScopes"`
}

type resourceGraphResponse struct {
	Data      []resourceGraphRow `json:"data"`
	SkipToken string             `json:"$skipToken"`
}

type resourceGraphRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

func (p Provider) discoverEligibilities(ctx context.Context) ([]pim.EligibleAssignment, error) {
	filter := url.QueryEscape("asTarget()")
	path := fmt.Sprintf("/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=%s&api-version=%s", filter, arm.AuthorizationAPIVersion)
	var assignments []pim.EligibleAssignment
	for path != "" {
		var response eligibilityResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("list eligible Azure role assignments: %w", err)
		}
		for _, item := range response.Value {
			assignments = append(assignments, normalizeEligibility(item))
		}
		path = response.NextLink
	}
	return assignments, nil
}

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	ctx, err := p.arm.PinAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate Azure Resource discovery: %w", err)
	}
	assignments, err := p.discoverEligibilities(ctx)
	if err != nil || len(assignments) == 0 {
		return assignments, err
	}
	scopes := assignmentScopes(assignments)
	activeByScope := make([][]roleAssignmentScheduleInstance, len(scopes))
	if err := forEachScope(ctx, scopes, func(ctx context.Context, index int, scope string) error {
		active, err := p.discoverActiveAssignments(ctx, scope)
		activeByScope[index] = active
		return err
	}); err != nil {
		return nil, err
	}
	var active []roleAssignmentScheduleInstance
	for _, scoped := range activeByScope {
		active = append(active, scoped...)
	}
	applyActiveState(assignments, active, time.Now().UTC())
	p.enrichResourceGroupSubscriptions(ctx, assignments)
	return assignments, nil
}

type subscriptionListResponse struct {
	Value    []subscriptionItem `json:"value"`
	NextLink string             `json:"nextLink"`
}

type subscriptionItem struct {
	SubscriptionID string `json:"subscriptionId"`
	DisplayName    string `json:"displayName"`
}

func subscriptionIDFromScope(scope string) string {
	prefix := scopePathPrefix(scope)
	lower := strings.ToLower(prefix)
	if !strings.HasPrefix(lower, "/subscriptions/") {
		return ""
	}
	rest := prefix[len("/subscriptions/"):]
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		rest = rest[:idx]
	}
	return strings.TrimSpace(rest)
}

func (p Provider) enrichResourceGroupSubscriptions(ctx context.Context, assignments []pim.EligibleAssignment) {
	subNames := make(map[string]string)
	needsLookup := false
	for _, a := range assignments {
		if a.Scope.Type == pim.ScopeTypeSubscription && strings.TrimSpace(a.Scope.DisplayName) != "" {
			if subID := subscriptionIDFromScope(a.Scope.ID); subID != "" {
				subNames[strings.ToLower(subID)] = strings.TrimSpace(a.Scope.DisplayName)
			}
		}
	}
	for i := range assignments {
		if assignments[i].Scope.Type != pim.ScopeTypeResourceGroup {
			continue
		}
		subID := subscriptionIDFromScope(assignments[i].Scope.ID)
		if subID == "" {
			continue
		}
		if name, ok := subNames[strings.ToLower(subID)]; ok {
			assignments[i].Scope.SubscriptionName = name
		} else {
			assignments[i].Scope.SubscriptionName = subID
			needsLookup = true
		}
	}
	if !needsLookup {
		return
	}
	path := "/subscriptions?api-version=2022-12-01"
	for path != "" {
		var resp subscriptionListResponse
		if err := p.arm.Get(ctx, path, &resp); err != nil {
			break
		}
		for _, item := range resp.Value {
			if id, name := strings.TrimSpace(item.SubscriptionID), strings.TrimSpace(item.DisplayName); id != "" && name != "" {
				subNames[strings.ToLower(id)] = name
			}
		}
		path = resp.NextLink
	}
	for i := range assignments {
		if assignments[i].Scope.Type != pim.ScopeTypeResourceGroup {
			continue
		}
		if subID := subscriptionIDFromScope(assignments[i].Scope.ID); subID != "" {
			if name, ok := subNames[strings.ToLower(subID)]; ok {
				assignments[i].Scope.SubscriptionName = name
			}
		}
	}
}

func (p Provider) Prepare(ctx context.Context, assignments []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	prepared := slices.Clone(assignments)
	if len(prepared) == 0 {
		return prepared, nil
	}
	ctx, err := p.arm.PinAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate Azure activation policy discovery: %w", err)
	}
	if err := p.applyPolicies(ctx, prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (p Provider) PrepareTargetScope(ctx context.Context, assignment pim.EligibleAssignment) (pim.EligibleAssignment, error) {
	ctx, err := p.arm.PinAccessToken(ctx)
	if err != nil {
		return pim.EligibleAssignment{}, fmt.Errorf("authenticate Azure target scope preparation: %w", err)
	}
	prepared := []pim.EligibleAssignment{assignment}
	active, err := p.discoverActiveAssignments(ctx, assignment.AzureScope)
	if err != nil {
		return pim.EligibleAssignment{}, err
	}
	prepared[0].Active = false
	prepared[0].ActiveUntil = nil
	applyActiveState(prepared, active, time.Now().UTC())
	if err := p.applyPolicies(ctx, prepared); err != nil {
		return pim.EligibleAssignment{}, err
	}
	return prepared[0], nil
}

func (p Provider) DiscoverChildScopes(ctx context.Context, parent pim.Scope) ([]pim.Scope, error) {
	prefix := scopePathPrefix(parent.ID)
	if prefix == "" || parent.Type == pim.ScopeTypeResourceGroup {
		return nil, nil
	}
	ctx, err := p.arm.PinAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate Azure child scope discovery: %w", err)
	}
	return p.listEligibleChildScopes(ctx, parent)
}

func (p Provider) listEligibleChildScopes(ctx context.Context, parent pim.Scope) ([]pim.Scope, error) {
	prefix := scopePathPrefix(parent.ID)
	if prefix == "" || parent.Type == pim.ScopeTypeResourceGroup {
		return nil, nil
	}
	query := fmt.Sprintf("api-version=%s", arm.AuthorizationAPIVersion)
	if parent.Type == pim.ScopeTypeSubscription {
		query = fmt.Sprintf("$filter=%s&%s", url.QueryEscape("resourceType eq 'resourcegroup'"), query)
	}
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/eligibleChildResources?%s", prefix, query)
	var children []pim.Scope
	for path != "" {
		var response childResourceResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return children, fmt.Errorf("list eligible child scopes at %s: %w", prefix, err)
		}
		for _, item := range response.Value {
			id := scopePathPrefix(item.ID)
			if id == "" {
				continue
			}
			scopeType := mapChildScopeType(item.Type, id)
			if scopeType == "" {
				continue
			}
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = resourceName(id)
			}
			child := pim.Scope{
				ID:          id,
				DisplayName: name,
				Type:        scopeType,
			}
			if scopeType == pim.ScopeTypeResourceGroup {
				if parent.Type == pim.ScopeTypeSubscription && strings.TrimSpace(parent.DisplayName) != "" {
					child.SubscriptionName = strings.TrimSpace(parent.DisplayName)
				} else {
					child.SubscriptionName = subscriptionIDFromScope(id)
				}
			}
			children = append(children, child)
		}
		path = response.NextLink
	}
	return children, nil
}

func (p Provider) DiscoverDescendantScopes(ctx context.Context, root pim.Scope) ([]pim.Scope, error) {
	prefix := scopePathPrefix(root.ID)
	if prefix == "" || root.Type == pim.ScopeTypeResourceGroup {
		return nil, nil
	}
	ctx, err := p.arm.PinAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate Azure descendant scope discovery: %w", err)
	}

	var (
		graphScopes []pim.Scope
		treeScopes  []pim.Scope
		treeErr     error
		wg          sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Best-effort fast path: Resource Graph returns only scopes with standing read access.
		graphScopes, _ = p.queryResourceGraphDescendants(ctx, root)
	}()
	go func() {
		defer wg.Done()
		treeScopes, treeErr = p.walkEligibleDescendants(ctx, root)
	}()
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	subNames := make(map[string]string)
	if root.Type == pim.ScopeTypeSubscription && strings.TrimSpace(root.DisplayName) != "" {
		if subID := subscriptionIDFromScope(root.ID); subID != "" {
			subNames[strings.ToLower(subID)] = strings.TrimSpace(root.DisplayName)
		}
	}
	byID := make(map[string]pim.Scope, len(graphScopes)+len(treeScopes))
	rootKey := strings.ToLower(prefix)
	add := func(scope pim.Scope) {
		key := strings.ToLower(scopePathPrefix(scope.ID))
		if scope.Type == pim.ScopeTypeSubscription && strings.TrimSpace(scope.DisplayName) != "" {
			if subID := subscriptionIDFromScope(scope.ID); subID != "" {
				subNames[strings.ToLower(subID)] = strings.TrimSpace(scope.DisplayName)
			}
		}
		if key == "" || key == rootKey {
			return
		}
		if existing, ok := byID[key]; ok {
			if existing.DisplayName == "" || strings.EqualFold(existing.DisplayName, resourceName(existing.ID)) {
				existing.DisplayName = scope.DisplayName
			}
			if existing.SubscriptionName == "" {
				existing.SubscriptionName = scope.SubscriptionName
			}
			byID[key] = existing
			return
		}
		byID[key] = scope
	}
	for _, scope := range graphScopes {
		add(scope)
	}
	for _, scope := range treeScopes {
		add(scope)
	}
	out := make([]pim.Scope, 0, len(byID))
	for _, scope := range byID {
		if scope.Type == pim.ScopeTypeResourceGroup {
			if subID := subscriptionIDFromScope(scope.ID); subID != "" {
				if name, ok := subNames[strings.ToLower(subID)]; ok {
					scope.SubscriptionName = name
				} else if scope.SubscriptionName == "" {
					scope.SubscriptionName = subID
				}
			}
		}
		out = append(out, scope)
	}
	slices.SortFunc(out, compareScopes)
	return out, treeErr
}

func compareScopes(a, b pim.Scope) int {
	rank := func(t pim.ScopeType) int {
		switch t {
		case pim.ScopeTypeManagementGroup:
			return 0
		case pim.ScopeTypeSubscription:
			return 1
		default:
			return 2
		}
	}
	if r := rank(a.Type) - rank(b.Type); r != 0 {
		return r
	}
	if c := strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)); c != 0 {
		return c
	}
	if c := strings.Compare(strings.ToLower(a.SubscriptionName), strings.ToLower(b.SubscriptionName)); c != 0 {
		return c
	}
	return strings.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID))
}

func (p Provider) queryResourceGraphDescendants(ctx context.Context, root pim.Scope) ([]pim.Scope, error) {
	req := resourceGraphRequest{
		Query: "ResourceContainers | where type in~ ('microsoft.management/managementgroups', 'microsoft.resources/subscriptions', 'microsoft.resources/subscriptions/resourcegroups') | project id, name, displayName = tostring(properties.displayName), type",
		Options: resourceGraphOptions{
			ResultFormat:       "objectArray",
			Top:                1000,
			AllowPartialScopes: true,
		},
	}
	prefix := scopePathPrefix(root.ID)
	switch root.Type {
	case pim.ScopeTypeSubscription:
		subID := strings.TrimPrefix(strings.ToLower(prefix), "/subscriptions/")
		if idx := strings.IndexByte(subID, '/'); idx >= 0 {
			subID = subID[:idx]
		}
		if subID == "" {
			return nil, nil
		}
		req.Subscriptions = []string{subID}
		req.Query = "ResourceContainers | where type =~ 'microsoft.resources/subscriptions/resourcegroups' | project id, name, displayName = '', type"
	case pim.ScopeTypeManagementGroup:
		if prefix != "/" {
			mgID := resourceName(prefix)
			if mgID != "" {
				req.ManagementGroups = []string{mgID}
			}
		}
	}

	path := fmt.Sprintf("/providers/Microsoft.ResourceGraph/resources?api-version=%s", resourceGraphAPIVersion)
	var out []pim.Scope
	for {
		var resp resourceGraphResponse
		if err := p.arm.Post(ctx, path, req, &resp); err != nil {
			return out, err
		}
		for _, row := range resp.Data {
			id := scopePathPrefix(row.ID)
			scopeType := mapChildScopeType(row.Type, id)
			if id == "" || scopeType == "" {
				continue
			}
			name := strings.TrimSpace(row.DisplayName)
			if name == "" {
				name = strings.TrimSpace(row.Name)
			}
			if name == "" {
				name = resourceName(id)
			}
			out = append(out, pim.Scope{ID: id, DisplayName: name, Type: scopeType})
		}
		if resp.SkipToken == "" {
			break
		}
		req.Options.SkipToken = resp.SkipToken
	}
	return out, nil
}

func (p Provider) walkEligibleDescendants(ctx context.Context, root pim.Scope) ([]pim.Scope, error) {
	semaphore := make(chan struct{}, maxConcurrentScopeRequests)
	var (
		mu       sync.Mutex
		visited  = map[string]bool{strings.ToLower(scopePathPrefix(root.ID)): true}
		results  []pim.Scope
		firstErr error
		wg       sync.WaitGroup
	)
	var visit func(pim.Scope)
	visit = func(parent pim.Scope) {
		defer wg.Done()
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}
		children, err := p.listEligibleChildScopes(ctx, parent)
		<-semaphore
		mu.Lock()
		if err != nil && firstErr == nil {
			firstErr = err
		}
		var drill []pim.Scope
		for _, child := range children {
			key := strings.ToLower(scopePathPrefix(child.ID))
			if visited[key] {
				continue
			}
			visited[key] = true
			results = append(results, child)
			if child.Type == pim.ScopeTypeManagementGroup || child.Type == pim.ScopeTypeSubscription {
				drill = append(drill, child)
			}
		}
		wg.Add(len(drill))
		mu.Unlock()
		for _, child := range drill {
			go visit(child)
		}
	}
	wg.Add(1)
	go visit(root)
	wg.Wait()
	return results, firstErr
}

func mapChildScopeType(apiType string, scope string) pim.ScopeType {
	switch strings.ToLower(strings.TrimSpace(apiType)) {
	case "managementgroup", "management group", "microsoft.management/managementgroups":
		return pim.ScopeTypeManagementGroup
	case "subscription", "microsoft.resources/subscriptions", "microsoft.subscription/subscriptions":
		return pim.ScopeTypeSubscription
	case "resourcegroup", "resource group", "microsoft.resources/subscriptions/resourcegroups":
		return pim.ScopeTypeResourceGroup
	}
	lower := strings.ToLower(scope)
	switch {
	case strings.Contains(lower, "/providers/microsoft.management/managementgroups/"):
		return pim.ScopeTypeManagementGroup
	case strings.Contains(lower, "/resourcegroups/") && strings.Count(lower, "/") <= 4:
		return pim.ScopeTypeResourceGroup
	case strings.HasPrefix(lower, "/subscriptions/") && strings.Count(lower, "/") == 2:
		return pim.ScopeTypeSubscription
	default:
		return ""
	}
}

func normalizeEligibility(item roleEligibilityScheduleInstance) pim.EligibleAssignment {
	scopeType := mapScopeType(item.Properties.ExpandedProperties.Scope.Type, item.Properties.Scope)
	scope := pim.Scope{
		ID:          item.Properties.Scope,
		DisplayName: item.Properties.ExpandedProperties.Scope.DisplayName,
		Type:        scopeType,
	}
	if scopeType == pim.ScopeTypeResourceGroup {
		scope.SubscriptionName = subscriptionIDFromScope(item.Properties.Scope)
	}
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
		Scope:                 scope,
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
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleAssignmentScheduleRequests/%s?api-version=%s", scopePathPrefix(request.Assignment.AzureScope), requestID, arm.AuthorizationAPIVersion)
	var response activationResponse
	if err := p.arm.Put(ctx, path, activationBody(request), &response); err != nil {
		return pim.ActivationResult{}, activation.WrapRetryable(err)
	}
	switch response.Properties.Status {
	case "Granted", "Provisioned", "ScheduleCreated":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusActivated, Message: response.Properties.Status}, nil
	case "PendingApproval", "PendingApprovalProvisioning", "PendingAdminDecision":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusPendingApproval, Message: response.Properties.Status}, nil
	case "Accepted", "PendingEvaluation", "PendingProvisioning", "ProvisioningStarted", "PendingScheduleCreation", "PendingExternalProvisioning", "AdminApproved", "PostApprovalExtensionPendingEvaluation", "PostApprovalExtensionAdminApproved":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusPendingProvisioning, Message: response.Properties.Status}, nil
	default:
		if response.Properties.Status == "" {
			return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusFailed, Message: "ARM activation response did not include a status; check PIM before resubmitting"}, nil
		}
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

func scopePathPrefix(scope string) string {
	scope = strings.TrimRight(strings.TrimSpace(scope), "/")
	if scope != "" && !strings.HasPrefix(scope, "/") {
		return "/" + scope
	}
	return scope
}
