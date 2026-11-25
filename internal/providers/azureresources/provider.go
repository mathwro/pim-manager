package azureresources

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/pim"
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
	Parent      string `json:"parent"`
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
	// Subscription labels are best-effort; list them while active state loads.
	listCtx, cancelListing := context.WithCancel(ctx)
	defer cancelListing()
	var (
		listedNames map[string]string
		listing     sync.WaitGroup
	)
	if needsSubscriptionListing(assignments) {
		listing.Add(1)
		go func() {
			defer listing.Done()
			listedNames = p.listSubscriptionNames(listCtx)
		}()
	}
	scopes := assignmentScopes(assignments)
	activeByScope := make([][]roleAssignmentScheduleInstance, len(scopes))
	if err := forEach(ctx, len(scopes), func(ctx context.Context, index int) error {
		active, err := p.discoverActiveAssignments(ctx, scopes[index])
		activeByScope[index] = active
		return err
	}); err != nil {
		cancelListing()
		listing.Wait()
		return nil, err
	}
	listing.Wait()
	var active []roleAssignmentScheduleInstance
	for _, scoped := range activeByScope {
		active = append(active, scoped...)
	}
	applyActiveState(assignments, active, time.Now().UTC())
	labelResourceGroupSubscriptions(assignments, listedNames)
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

// assignmentSubscriptionNames maps lowercase subscription IDs to the names of
// subscription-scoped assignments.
func assignmentSubscriptionNames(assignments []pim.EligibleAssignment) map[string]string {
	names := make(map[string]string)
	for _, a := range assignments {
		if a.Scope.Type == pim.ScopeTypeSubscription && strings.TrimSpace(a.Scope.DisplayName) != "" {
			if subID := subscriptionIDFromScope(a.Scope.ID); subID != "" {
				names[strings.ToLower(subID)] = strings.TrimSpace(a.Scope.DisplayName)
			}
		}
	}
	return names
}

func needsSubscriptionListing(assignments []pim.EligibleAssignment) bool {
	names := assignmentSubscriptionNames(assignments)
	for _, a := range assignments {
		if a.Scope.Type != pim.ScopeTypeResourceGroup {
			continue
		}
		if subID := subscriptionIDFromScope(a.Scope.ID); subID != "" {
			if _, ok := names[strings.ToLower(subID)]; !ok {
				return true
			}
		}
	}
	return false
}

// listSubscriptionNames is best-effort: a failed page keeps the names already listed.
func (p Provider) listSubscriptionNames(ctx context.Context) map[string]string {
	names := make(map[string]string)
	path := "/subscriptions?api-version=2022-12-01"
	for path != "" {
		var resp subscriptionListResponse
		if err := p.arm.Get(ctx, path, &resp); err != nil {
			break
		}
		for _, item := range resp.Value {
			if id, name := strings.TrimSpace(item.SubscriptionID), strings.TrimSpace(item.DisplayName); id != "" && name != "" {
				names[strings.ToLower(id)] = name
			}
		}
		path = resp.NextLink
	}
	return names
}

// labelResourceGroupSubscriptions names each resource group's subscription,
// preferring listed names, then subscription assignments, then the ID.
func labelResourceGroupSubscriptions(assignments []pim.EligibleAssignment, listed map[string]string) {
	names := assignmentSubscriptionNames(assignments)
	maps.Copy(names, listed)
	for i := range assignments {
		if assignments[i].Scope.Type != pim.ScopeTypeResourceGroup {
			continue
		}
		subID := subscriptionIDFromScope(assignments[i].Scope.ID)
		if subID == "" {
			continue
		}
		if name, ok := names[strings.ToLower(subID)]; ok {
			assignments[i].Scope.SubscriptionName = name
		} else {
			assignments[i].Scope.SubscriptionName = subID
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
	var active []roleAssignmentScheduleInstance
	if err := forEach(ctx, 2, func(ctx context.Context, index int) error {
		if index == 0 {
			var err error
			active, err = p.discoverActiveAssignments(ctx, assignment.AzureScope)
			return err
		}
		return p.applyPolicies(ctx, prepared)
	}); err != nil {
		return pim.EligibleAssignment{}, err
	}
	prepared[0].Active = false
	prepared[0].ActiveUntil = nil
	applyActiveState(prepared, active, time.Now().UTC())
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
				ParentID:    prefix,
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

// IndexDescendantScopes lists the management groups, subscriptions and
// resource groups below root that Resource Graph can read, excluding root.
// A failed page returns the scopes already indexed with the error.
func (p Provider) IndexDescendantScopes(ctx context.Context, root pim.Scope) ([]pim.Scope, error) {
	prefix := scopePathPrefix(root.ID)
	if prefix == "" || root.Type == pim.ScopeTypeResourceGroup {
		return nil, nil
	}
	ctx, err := p.arm.PinAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate Azure descendant scope index: %w", err)
	}
	scopes, err := p.queryResourceGraphDescendants(ctx, root)
	if err != nil {
		err = fmt.Errorf("index descendant scopes of %s: %w", prefix, err)
	}
	subNames := make(map[string]string)
	if root.Type == pim.ScopeTypeSubscription && strings.TrimSpace(root.DisplayName) != "" {
		if subID := subscriptionIDFromScope(prefix); subID != "" {
			subNames[strings.ToLower(subID)] = strings.TrimSpace(root.DisplayName)
		}
	}
	rootKey := strings.ToLower(prefix)
	out := scopes[:0]
	for _, scope := range scopes {
		if strings.ToLower(scope.ID) == rootKey {
			continue
		}
		if scope.Type == pim.ScopeTypeSubscription {
			if subID := subscriptionIDFromScope(scope.ID); subID != "" {
				subNames[strings.ToLower(subID)] = scope.DisplayName
			}
		}
		out = append(out, scope)
	}
	for i := range out {
		if out[i].Type != pim.ScopeTypeResourceGroup {
			continue
		}
		subID := subscriptionIDFromScope(out[i].ID)
		if name, ok := subNames[strings.ToLower(subID)]; ok {
			out[i].SubscriptionName = name
		} else {
			out[i].SubscriptionName = subID
		}
	}
	slices.SortFunc(out, compareScopes)
	return out, err
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

const managementGroupPathPrefix = "/providers/Microsoft.Management/managementGroups/"

// resourceGraphParentColumn yields the full parent ID for management groups
// and the parent management group name for subscriptions.
const resourceGraphParentColumn = "extend parent = case(type =~ 'microsoft.management/managementgroups', tostring(properties.details.parent.id), type =~ 'microsoft.resources/subscriptions', tostring(properties.managementGroupAncestorsChain[0].name), '')"

func (p Provider) queryResourceGraphDescendants(ctx context.Context, root pim.Scope) ([]pim.Scope, error) {
	req := resourceGraphRequest{
		Query: "ResourceContainers | where type in~ ('microsoft.management/managementgroups', 'microsoft.resources/subscriptions', 'microsoft.resources/subscriptions/resourcegroups') | " +
			resourceGraphParentColumn + " | project id, name, displayName = tostring(properties.displayName), type, parent",
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
		// Resource group parents come from their IDs.
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
			out = append(out, pim.Scope{ID: id, DisplayName: name, Type: scopeType, ParentID: resourceGraphParentID(scopeType, id, row.Parent)})
		}
		if resp.SkipToken == "" {
			break
		}
		req.Options.SkipToken = resp.SkipToken
	}
	return out, nil
}

func resourceGraphParentID(scopeType pim.ScopeType, id string, parent string) string {
	if scopeType == pim.ScopeTypeResourceGroup {
		if subID := subscriptionIDFromScope(id); subID != "" {
			return "/subscriptions/" + subID
		}
		return ""
	}
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return ""
	}
	if !strings.Contains(parent, "/") {
		return managementGroupPathPrefix + parent
	}
	return scopePathPrefix(parent)
}

// WalkDescendantScopes recursively lists eligible child scopes below root. It
// skips listing children of subscriptions in indexed, whose resource groups
// Resource Graph already returned; management groups are always walked.
// It returns the scopes found so far with the first error.
func (p Provider) WalkDescendantScopes(ctx context.Context, root pim.Scope, indexed []pim.Scope) ([]pim.Scope, error) {
	prefix := scopePathPrefix(root.ID)
	if prefix == "" || root.Type == pim.ScopeTypeResourceGroup {
		return nil, nil
	}
	indexedSubscriptions := make(map[string]bool)
	for _, scope := range indexed {
		if scope.Type == pim.ScopeTypeSubscription {
			indexedSubscriptions[strings.ToLower(scopePathPrefix(scope.ID))] = true
		}
	}
	isIndexed := func(scope pim.Scope) bool {
		return scope.Type == pim.ScopeTypeSubscription && indexedSubscriptions[strings.ToLower(scopePathPrefix(scope.ID))]
	}
	if isIndexed(root) {
		return nil, nil
	}
	ctx, err := p.arm.PinAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate Azure descendant scope walk: %w", err)
	}

	semaphore := make(chan struct{}, maxConcurrentScopeRequests)
	var (
		mu       sync.Mutex
		visited  = map[string]bool{strings.ToLower(prefix): true}
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
			if child.Type == pim.ScopeTypeManagementGroup || (child.Type == pim.ScopeTypeSubscription && !isIndexed(child)) {
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
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	slices.SortFunc(results, compareScopes)
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
