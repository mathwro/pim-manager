package pim

import (
	"fmt"
	"time"
)

type AssignmentSource string

const (
	AssignmentSourceEntra         AssignmentSource = "entra"
	AssignmentSourceAzureResource AssignmentSource = "azure_resource"
	AssignmentSourceGroup         AssignmentSource = "group"
)

type AssignmentKind string

const (
	AssignmentKindDirectoryRole AssignmentKind = "directory_role"
	AssignmentKindAzureRole     AssignmentKind = "azure_role"
	AssignmentKindGroupMember   AssignmentKind = "group_member"
	AssignmentKindGroupOwner    AssignmentKind = "group_owner"
)

type ScopeType string

const (
	ScopeTypeTenant          ScopeType = "Tenant"
	ScopeTypeManagementGroup ScopeType = "Management Group"
	ScopeTypeSubscription    ScopeType = "Subscription"
	ScopeTypeResourceGroup   ScopeType = "Resource Group"
	ScopeTypeGroup           ScopeType = "Group"
)

type Scope struct {
	ID          string
	DisplayName string
	Type        ScopeType
}

type ActivationPolicy struct {
	MaximumDurationISO    string
	JustificationRequired bool
	MFARequired           bool
}

type EligibleAssignment struct {
	ID                    string
	Source                AssignmentSource
	Kind                  AssignmentKind
	DisplayName           string
	PrincipalID           string
	RoleDefinitionID      string
	DirectoryScopeID      string
	AppScopeID            string
	GroupID               string
	AccessID              string
	AzureScope            string
	EligibilityScheduleID string
	Scope                 Scope
	Condition             string
	ConditionVersion      string
	Active                bool
	ActiveUntil           *time.Time
	ActivationPolicy      ActivationPolicy
}

func (a EligibleAssignment) DisplayScope() string {
	if a.Scope.DisplayName == "" {
		return string(a.Scope.Type)
	}
	return fmt.Sprintf("%s: %s", a.Scope.Type, a.Scope.DisplayName)
}

type ActivationRequest struct {
	Assignment    EligibleAssignment
	Justification string
	DurationISO   string
}

type ActivationStatus string

const (
	ActivationStatusActivated       ActivationStatus = "activated"
	ActivationStatusPendingApproval ActivationStatus = "pending_approval"
	ActivationStatusFailed          ActivationStatus = "failed"
)

type ActivationResult struct {
	Assignment EligibleAssignment
	Status     ActivationStatus
	Message    string
	Retryable  bool
}

func (r ActivationResult) Success() bool {
	return r.Status == ActivationStatusActivated
}

func (r ActivationResult) PendingApproval() bool {
	return r.Status == ActivationStatusPendingApproval
}

func (r ActivationResult) Failure() bool {
	return r.Status == ActivationStatusFailed
}

func (r ActivationResult) CanRetry() bool {
	return r.Failure() && r.Retryable
}
