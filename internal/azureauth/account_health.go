package azureauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
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
