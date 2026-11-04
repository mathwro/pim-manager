// Package graphauth owns an in-memory, tenant-bound native Microsoft Graph session.
package graphauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
	"github.com/google/uuid"
	"github.com/mathwro/pim-manager/internal/azureauth"
)

const graphResource = "https://graph.microsoft.com"

var workflowScopes = []string{
	graphResource + "/User.Read",
	graphResource + "/RoleEligibilitySchedule.Read.Directory",
	graphResource + "/RoleAssignmentSchedule.ReadWrite.Directory",
	graphResource + "/PrivilegedEligibilitySchedule.Read.AzureADGroup",
	graphResource + "/PrivilegedAssignmentSchedule.ReadWrite.AzureADGroup",
	graphResource + "/RoleManagementPolicy.Read.Directory",
	graphResource + "/RoleManagementPolicy.Read.AzureADGroup",
}

var ErrReconnect = errors.New("Microsoft Graph session is not connected or its identity changed; select Connect Microsoft Graph again")

type ARMAuthenticationSource interface {
	ARMAuthentication(context.Context, bool, string) (azureauth.ARMAuthentication, error)
}

// tokenClient isolates only MSAL's network/browser boundary; identity checks stay in Session.
type tokenClient interface {
	acquireInteractive(context.Context, []string, string, string) (public.AuthResult, error)
	acquireSilent(context.Context, []string, public.Account, string) (public.AuthResult, error)
}

type connection struct {
	tenant    string
	principal string
	client    tokenClient
	account   public.Account
	loginHint string
}

type Session struct {
	cli           ARMAuthenticationSource
	newClient     func(string) (tokenClient, error)
	mu            sync.Mutex
	generation    uint64
	tenant        string
	nextOperation uint64
	active        map[uint64]context.CancelFunc
	connected     *connection
}

func NewSession(cli ARMAuthenticationSource) *Session {
	return &Session{cli: cli, newClient: newMSALClient, active: make(map[uint64]context.CancelFunc)}
}

// Reset cancels in-flight operations without waiting for network or browser callbacks.
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked()
}

func (s *Session) resetLocked() {
	s.generation++
	s.connected = nil
	s.tenant = ""
	for id, cancel := range s.active {
		cancel()
		delete(s.active, id)
	}
}

// Connected checks local session state only; it never acquires tokens or opens a browser.
func (s *Session) Connected(ctx context.Context) bool {
	tenant, err := selectedTenant(ctx)
	if err != nil || ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected != nil && s.connected.tenant == tenant
}

type operation struct {
	session    *Session
	ctx        context.Context
	cancel     context.CancelFunc
	tenant     string
	generation uint64
	id         uint64
	connection *connection
}

func (s *Session) begin(ctx context.Context, connect bool) (*operation, error) {
	tenant, err := selectedTenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current := s.connected
	if (s.tenant != "" && s.tenant != tenant) || (current != nil && current.tenant != tenant) {
		return nil, ErrReconnect
	}
	if connect {
		s.resetLocked()
		s.tenant = tenant
		current = nil
	} else if current == nil {
		return nil, ErrReconnect
	}
	opCtx, cancel := context.WithCancel(ctx)
	s.nextOperation++
	id := s.nextOperation
	s.active[id] = cancel
	return &operation{session: s, ctx: opCtx, cancel: cancel, tenant: tenant, generation: s.generation, id: id, connection: current}, nil
}

// finish releases the operation. Failures reset the session, except when a read's
// caller canceled it: that read proved nothing about the established connection.
func (o *operation) finish(err error, keepOnCallerCancel bool) error {
	canceled := o.ctx.Err() != nil
	if err == nil {
		err = o.ctx.Err()
	}
	o.cancel()
	s := o.session
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, o.id)
	// A reset bumps the generation, so a canceled same-generation operation was canceled by its caller.
	callerCanceled := canceled && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
	if err != nil && s.generation == o.generation && !(keepOnCallerCancel && callerCanceled) {
		s.resetLocked()
	}
	return err
}

func (o *operation) commit(current *connection, result public.AuthResult) error {
	s := o.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := o.ctx.Err(); err != nil {
		return err
	}
	if s.generation != o.generation {
		return ErrReconnect
	}
	updated := *current
	updated.account = result.Account
	s.connected = &updated
	return nil
}

func selectedTenant(ctx context.Context) (string, error) {
	tenant := strings.TrimSpace(azureauth.TenantFromContext(ctx))
	id, err := uuid.Parse(tenant)
	if err != nil || id == uuid.Nil {
		return "", errors.New("select a valid Azure tenant before connecting Microsoft Graph")
	}
	return id.String(), nil
}

func (s *Session) armIdentity(ctx context.Context, tenant string) (tokenClaims, error) {
	if s.cli == nil {
		return tokenClaims{}, errors.New("Microsoft Graph authentication requires the existing Azure CLI ARM session")
	}
	auth, err := s.cli.ARMAuthentication(ctx, false, "")
	if err != nil {
		return tokenClaims{}, authenticationError(ctx, "check Azure CLI ARM identity", err)
	}
	if err := ctx.Err(); err != nil {
		return tokenClaims{}, err
	}
	claims, err := decodeClaims(auth.AccessToken)
	if err != nil {
		return tokenClaims{}, errors.New("cannot inspect Azure CLI ARM identity; sign in to the selected tenant and reconnect Microsoft Graph")
	}
	if claims.Principal == "" || !strings.EqualFold(claims.Principal, auth.PrincipalID) || !strings.EqualFold(claims.Tenant, tenant) {
		return tokenClaims{}, fmt.Errorf("Azure CLI ARM identity does not match the selected tenant and checked principal: %w", ErrReconnect)
	}
	return claims, nil
}

func (s *Session) checkARM(ctx context.Context, current *connection) error {
	identity, err := s.armIdentity(ctx, current.tenant)
	if err != nil {
		return err
	}
	if !strings.EqualFold(identity.Principal, current.principal) {
		return fmt.Errorf("Azure CLI principal changed: %w", ErrReconnect)
	}
	return nil
}

// Connect is the explicit consent entry point. It always signs in through the
// browser; the TUI offers it only while the session is disconnected.
func (s *Session) Connect(ctx context.Context) (err error) {
	op, err := s.begin(ctx, true)
	if err != nil {
		return err
	}
	defer func() { err = op.finish(err, false) }()
	identity, err := s.armIdentity(op.ctx, op.tenant)
	if err != nil {
		return err
	}
	client, err := s.newClient(op.tenant)
	if err != nil {
		return authenticationError(op.ctx, "create Microsoft Graph client", err)
	}
	current := &connection{tenant: op.tenant, principal: identity.Principal, client: client, loginHint: identity.username()}
	result, err := client.acquireInteractive(op.ctx, workflowScopes, "", current.browserHint())
	if err != nil {
		return authenticationError(op.ctx, "connect Microsoft Graph", err)
	}
	if _, err = validateResult(result, current); err != nil {
		return err
	}
	if err = s.checkARM(op.ctx, current); err != nil {
		return err
	}
	return op.commit(current, result)
}

// AccessToken performs silent acquisition only. Interactive recovery requires Connect.
func (s *Session) AccessToken(ctx context.Context, resource string) (token string, err error) {
	if strings.TrimRight(resource, "/") != graphResource {
		return "", errors.New("native Microsoft Graph credentials cannot be used for another resource")
	}
	op, err := s.begin(ctx, false)
	if err != nil {
		return "", err
	}
	defer func() {
		err = op.finish(err, true)
		if err != nil {
			token = ""
		}
	}()
	current := op.connection
	result, err := current.client.acquireSilent(op.ctx, workflowScopes, current.account, "")
	if err != nil {
		return "", authenticationError(op.ctx, "silently acquire Microsoft Graph token; reconnect Microsoft Graph if interaction is required", err)
	}
	if _, err = validateResult(result, current); err != nil {
		return "", err
	}
	if err = op.commit(current, result); err != nil {
		return "", err
	}
	return result.AccessToken, nil
}

// Authentication returns the exact token checked for this activation. Callers must
// pin AccessToken to submission instead of acquiring another token afterwards.
func (s *Session) Authentication(ctx context.Context, mfaRequired bool, authenticationContext string) (auth azureauth.ARMAuthentication, err error) {
	op, err := s.begin(ctx, false)
	if err != nil {
		return auth, err
	}
	defer func() {
		err = op.finish(err, false)
		if err != nil {
			auth = azureauth.ARMAuthentication{}
		}
	}()
	current := op.connection
	if err = s.checkARM(op.ctx, current); err != nil {
		return auth, err
	}
	result, err := current.client.acquireSilent(op.ctx, workflowScopes, current.account, "")
	if err != nil {
		return auth, authenticationError(op.ctx, "check Microsoft Graph token; reconnect Microsoft Graph if interaction is required", err)
	}
	claims, err := validateResult(result, current)
	if err != nil {
		return auth, err
	}
	authenticationContext = strings.TrimSpace(authenticationContext)
	if !claims.satisfies(mfaRequired, authenticationContext) {
		challenge := policyClaims(mfaRequired, authenticationContext)
		result, err = current.client.acquireSilent(op.ctx, workflowScopes, current.account, challenge)
		if err == nil {
			claims, err = validateResult(result, current)
			if err != nil {
				return auth, err
			}
		}
		if ctxErr := op.ctx.Err(); ctxErr != nil {
			return auth, ctxErr
		}
		if err != nil || !claims.satisfies(mfaRequired, authenticationContext) {
			if err = s.checkARM(op.ctx, current); err != nil {
				return auth, err
			}
			result, err = current.client.acquireInteractive(op.ctx, workflowScopes, challenge, current.browserHint())
			if err != nil {
				return auth, authenticationError(op.ctx, "complete Microsoft Graph policy authentication", err)
			}
			claims, err = validateResult(result, current)
			if err != nil {
				return auth, err
			}
		}
		if !claims.satisfies(mfaRequired, authenticationContext) {
			return auth, errors.New("Microsoft Graph token does not demonstrate the required MFA or authentication context; reconnect and satisfy the tenant policy")
		}
	}
	if err = s.checkARM(op.ctx, current); err != nil {
		return auth, err
	}
	if err = op.commit(current, result); err != nil {
		return auth, err
	}
	return azureauth.ARMAuthentication{AccessToken: result.AccessToken, PrincipalID: current.principal, Satisfied: true}, nil
}

type tokenClaims struct {
	Tenant            string   `json:"tid"`
	Principal         string   `json:"oid"`
	Audience          string   `json:"aud"`
	Expires           int64    `json:"exp"`
	Scopes            string   `json:"scp"`
	Methods           []string `json:"amr"`
	Contexts          []string `json:"acrs"`
	PreferredUsername string   `json:"preferred_username"`
	UPN               string   `json:"upn"`
	UniqueName        string   `json:"unique_name"`
}

func (c tokenClaims) username() string {
	for _, value := range []string{c.PreferredUsername, c.UPN, c.UniqueName} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (c *connection) browserHint() string {
	if c.loginHint != "" {
		return c.loginHint
	}
	return strings.TrimSpace(c.account.PreferredUsername)
}

func decodeClaims(token string) (tokenClaims, error) {
	var claims tokenClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, errors.New("token identity claims are not readable")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, errors.New("token identity claims are not readable")
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, errors.New("token identity claims are not readable")
	}
	return claims, nil
}

func validateResult(result public.AuthResult, current *connection) (tokenClaims, error) {
	claims, err := decodeClaims(result.AccessToken)
	if err != nil {
		return claims, fmt.Errorf("inspect Microsoft Graph token: %w; reconnect Microsoft Graph", err)
	}
	if result.Account.HomeAccountID == "" || !strings.EqualFold(claims.Tenant, current.tenant) || !strings.EqualFold(claims.Principal, current.principal) ||
		!strings.EqualFold(result.Account.Realm, current.tenant) || !strings.EqualFold(result.Account.LocalAccountID, current.principal) ||
		!strings.EqualFold(result.IDToken.TenantID, current.tenant) || !strings.EqualFold(result.IDToken.Oid, current.principal) {
		return claims, fmt.Errorf("Microsoft Graph token/account identity does not match the selected tenant and Azure CLI principal: %w", ErrReconnect)
	}
	if !strings.EqualFold(strings.TrimRight(claims.Audience, "/"), graphResource) && !strings.EqualFold(claims.Audience, "00000003-0000-0000-c000-000000000000") {
		return claims, errors.New("Microsoft Graph returned a token for another audience; reconnect Microsoft Graph")
	}
	if claims.Expires <= time.Now().Unix() {
		return claims, errors.New("Microsoft Graph returned an expired token; reconnect Microsoft Graph")
	}
	tokenScopes := strings.Fields(claims.Scopes)
	for _, required := range workflowScopes {
		if !containsScope(result.GrantedScopes, required) || !containsScope(tokenScopes, required) {
			return claims, fmt.Errorf("Microsoft Graph consent is missing delegated permission %s; reconnect and obtain tenant consent", strings.TrimPrefix(required, graphResource+"/"))
		}
	}
	return claims, nil
}

func containsScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if strings.EqualFold(strings.TrimPrefix(scope, graphResource+"/"), strings.TrimPrefix(required, graphResource+"/")) {
			return true
		}
	}
	return false
}

func (c tokenClaims) satisfies(mfa bool, authenticationContext string) bool {
	if mfa && !contains(c.Methods, "mfa") && !contains(c.Methods, "ngcmfa") {
		return false
	}
	return authenticationContext == "" || contains(c.Contexts, authenticationContext)
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func policyClaims(mfa bool, authenticationContext string) string {
	type requirement struct {
		Essential bool     `json:"essential"`
		Value     string   `json:"value,omitempty"`
		Values    []string `json:"values,omitempty"`
	}
	claims := make(map[string]requirement, 2)
	if mfa {
		claims["amr"] = requirement{Essential: true, Values: []string{"mfa"}}
	}
	if authenticationContext != "" {
		claims["acrs"] = requirement{Essential: true, Value: authenticationContext}
	}
	payload, _ := json.Marshal(struct {
		AccessToken map[string]requirement `json:"access_token"`
	}{AccessToken: claims})
	return string(payload)
}
