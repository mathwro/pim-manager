package azureauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mathwro/pim-manager/internal/arm"
)

// AccountHealth identifies the account found by a live Microsoft Graph self lookup.
// Finding the account does not establish that its roles or sign-in are active.
type AccountHealth struct {
	TenantID          string
	PrincipalID       string
	DisplayName       string
	UserPrincipalName string
}

// ErrAccountUnavailable indicates definite account absence or an unusable sign-in.
// Other probe errors leave account health unverified.
var ErrAccountUnavailable = errors.New("account unavailable")

// CheckAccount performs a non-interactive, tenant-scoped live self lookup. Its
// fifteen-second background deadline covers Azure CLI startup, token acquisition and Graph HTTP.
func (c CLI) CheckAccount(ctx context.Context) (AccountHealth, error) {
	return c.checkAccount(ctx, &http.Client{})
}

func (c CLI) checkAccount(ctx context.Context, client *http.Client) (AccountHealth, error) {
	tenantID := TenantFromContext(ctx)
	if tenantID == "" {
		return AccountHealth{}, errors.New("check account: no tenant selected")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	token, err := c.AccessToken(ctx, "https://graph.microsoft.com/")
	if err != nil {
		if ctx.Err() != nil {
			return AccountHealth{}, fmt.Errorf("Graph token acquisition: %w", ctx.Err())
		}
		return AccountHealth{}, fmt.Errorf("Graph token acquisition: %w", err)
	}

	claims := graphIdentityClaims(token)
	if claims.TenantID != "" && !strings.EqualFold(claims.TenantID, tenantID) {
		return AccountHealth{}, errors.New("check account: Graph token tenant does not match the selected tenant")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me?$select=id,displayName,userPrincipalName", nil)
	if err != nil {
		return AccountHealth{}, fmt.Errorf("create Graph account request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return AccountHealth{}, fmt.Errorf("Graph self lookup: %w", err)
	}
	defer response.Body.Close()
	if ctx.Err() != nil {
		return AccountHealth{}, fmt.Errorf("Graph self lookup: %w", ctx.Err())
	}
	if response.StatusCode != http.StatusOK {
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		// An unstructured response is still actionable by HTTP status, but does
		// not establish account absence (except for a missing self resource).
		_ = json.NewDecoder(response.Body).Decode(&payload)
		graphErr := fmt.Errorf("Graph self lookup: HTTP %d: %s: %s", response.StatusCode, payload.Error.Code, payload.Error.Message)
		if graphInteractionChallenge(response.Header.Get("Www-Authenticate"), payload.Error.Message) {
			if health, ok := c.checkARMAccountFallback(ctx, client, tenantID, claims.PrincipalID); ok {
				return health, nil
			}
			return AccountHealth{}, graphErr
		}
		if response.StatusCode == http.StatusNotFound ||
			(response.StatusCode == http.StatusUnauthorized && strings.EqualFold(payload.Error.Code, "InvalidAuthenticationToken")) ||
			accountSignInUnavailable(payload.Error.Message) {
			return AccountHealth{}, fmt.Errorf("%w: %w", ErrAccountUnavailable, graphErr)
		}
		return AccountHealth{}, graphErr
	}
	var self struct {
		ID                string `json:"id"`
		DisplayName       string `json:"displayName"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := json.NewDecoder(response.Body).Decode(&self); err != nil {
		return AccountHealth{}, fmt.Errorf("Graph self lookup response: %w", err)
	}
	principalID := strings.TrimSpace(self.ID)
	if principalID == "" {
		return AccountHealth{}, errors.New("check Graph account: self response has no principal ID")
	}
	if claims.PrincipalID != "" && !strings.EqualFold(claims.PrincipalID, principalID) {
		return AccountHealth{}, errors.New("check account: Graph self principal does not match the token principal")
	}
	return AccountHealth{
		TenantID:          tenantID,
		PrincipalID:       principalID,
		DisplayName:       strings.TrimSpace(self.DisplayName),
		UserPrincipalName: strings.TrimSpace(self.UserPrincipalName),
	}, nil
}

type graphIdentity struct {
	TenantID    string `json:"tid"`
	PrincipalID string `json:"oid"`
}

func graphIdentityClaims(token string) graphIdentity {
	// Graph tokens may be opaque. Readable claims are only consistency guards,
	// not signature validation; Graph validates the token and ARM stays authoritative.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return graphIdentity{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return graphIdentity{}
	}
	var claims graphIdentity
	if json.Unmarshal(payload, &claims) != nil {
		return graphIdentity{}
	}
	return claims
}

func accountSignInUnavailable(message string) bool {
	message = strings.ToLower(message)
	if start := strings.Index(message, "aadsts"); start >= 0 {
		code := message[start+len("aadsts"):]
		end := 0
		for end < len(code) && code[end] >= '0' && code[end] <= '9' {
			end++
		}
		// Unknown STS errors include consent, Graph restrictions and transient
		// failures; even their az-login guidance must not invalidate the account.
		switch code[:end] {
		case "50020", "50034", "50053", "50055", "50057", "50058", "50173", "70008", "700082", "700084":
			return true
		default:
			return false
		}
	}
	return azureCLILoginRequired(errors.New(message)) ||
		strings.Contains(message, "user account is disabled") ||
		strings.Contains(message, "user account does not exist")
}

// graphInteractionChallenge detects Graph Conditional Access / Continuous Access
// Evaluation (CAE) challenges on the Azure CLI Graph token (for example,
// InteractionRequired / TokenCreatedWithOutdatedPolicies after PIM role changes).
// Because Azure CLI may still hold a valid ARM token and Entra/Groups use their
// own MSAL browser session, a Graph CAE challenge leaves the account probe
// unverified rather than wiping the user's ARM workflow.
func graphInteractionChallenge(wwwAuthenticate string, message string) bool {
	lowerAuth := strings.ToLower(wwwAuthenticate)
	if strings.Contains(lowerAuth, "insufficient_claims") || strings.Contains(lowerAuth, "claims=") {
		return true
	}
	lowerMsg := strings.ToLower(message)
	return strings.Contains(lowerMsg, "continuous access evaluation") ||
		strings.Contains(lowerMsg, "interactionrequired") ||
		strings.Contains(lowerMsg, "tokencreatedwithoutdatedpolicies")
}

type armIdentity struct {
	TenantID          string `json:"tid"`
	PrincipalID       string `json:"oid"`
	DisplayName       string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	UPN               string `json:"upn"`
	UniqueName        string `json:"unique_name"`
}

func (i armIdentity) userPrincipalName() string {
	for _, value := range []string{i.PreferredUsername, i.UPN, i.UniqueName} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func armIdentityClaims(token string) armIdentity {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return armIdentity{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return armIdentity{}
	}
	var claims armIdentity
	if json.Unmarshal(payload, &claims) != nil {
		return armIdentity{}
	}
	return claims
}

// checkARMAccountFallback verifies live sign-in against ARM when Microsoft Graph
// rejects Azure CLI's cached Graph token with a CAE claims challenge (which `az`
// cannot answer non-interactively) while the ARM session remains valid.
func (c CLI) checkARMAccountFallback(ctx context.Context, client *http.Client, tenantID string, graphPrincipalID string) (AccountHealth, bool) {
	token, err := c.AccessToken(ctx, arm.Resource)
	if err != nil {
		return AccountHealth{}, false
	}
	claims := armIdentityClaims(token)
	principalID := strings.TrimSpace(claims.PrincipalID)
	if principalID == "" {
		return AccountHealth{}, false
	}
	if claims.TenantID != "" && !strings.EqualFold(claims.TenantID, tenantID) {
		return AccountHealth{}, false
	}
	if graphPrincipalID != "" && !strings.EqualFold(graphPrincipalID, principalID) {
		return AccountHealth{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tenantListURL, nil)
	if err != nil {
		return AccountHealth{}, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return AccountHealth{}, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return AccountHealth{}, false
	}
	return AccountHealth{
		TenantID:          tenantID,
		PrincipalID:       principalID,
		DisplayName:       strings.TrimSpace(claims.DisplayName),
		UserPrincipalName: claims.userPrincipalName(),
	}, true
}
