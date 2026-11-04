package graphauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"

	msalerrors "github.com/AzureAD/microsoft-authentication-library-for-go/apps/errors"
	"github.com/google/uuid"
	"github.com/mathwro/pim-manager/internal/azureauth"
)

var aadstsCode = regexp.MustCompile(`\bAADSTS[0-9]+\b`)

// MSAL errors can include raw HTTP request/response data. Keep only allowlisted
// diagnostics; never wrap the original error or print token/claims-bearing bodies.
func authenticationError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	var browser *browserError
	if errors.As(err, &browser) {
		return fmt.Errorf("%s: %s", operation, browser.Error())
	}
	message := err.Error()
	details := make([]string, 0, 6)
	var call msalerrors.CallErr
	if errors.As(err, &call) && call.Resp != nil {
		details = append(details, "HTTP "+strconv.Itoa(call.Resp.StatusCode))
		if call.Resp.Body != nil {
			body, readErr := io.ReadAll(io.LimitReader(call.Resp.Body, 64<<10))
			if readErr == nil {
				var payload struct {
					Error         string `json:"error"`
					Description   string `json:"error_description"`
					CorrelationID string `json:"correlation_id"`
					TraceID       string `json:"trace_id"`
				}
				if json.Unmarshal(body, &payload) == nil {
					message += " " + payload.Error + " " + payload.Description
					for _, id := range []struct{ name, value string }{{"correlation", payload.CorrelationID}, {"trace", payload.TraceID}} {
						if parsed, parseErr := uuid.Parse(id.value); parseErr == nil {
							details = append(details, id.name+" "+parsed.String())
						}
					}
				}
			}
		}
	}
	codes := aadstsCode.FindAllString(message, -1)
	seen := make(map[string]bool, len(codes))
	for _, code := range codes {
		if !seen[code] {
			details = append(details, code)
			seen[code] = true
		}
	}
	for _, code := range []string{"consent_required", "interaction_required", "login_required", "invalid_grant", "access_denied", "invalid_scope", "unauthorized_client", "temporarily_unavailable"} {
		if strings.Contains(message, code) {
			details = append(details, code)
		}
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			details = append(details, "network timeout")
		} else {
			details = append(details, "network connection failed")
		}
	}
	action := "check tenant consent and Conditional Access policy, then try Connect Microsoft Graph again"
	if errors.Is(err, azureauth.ErrNotLoggedIn) || strings.Contains(strings.ToLower(message), "az login") || strings.Contains(strings.ToLower(message), "not logged in") {
		action = "run az login"
		if tenant, tenantErr := selectedTenant(ctx); tenantErr == nil {
			action += " --tenant " + tenant
		}
		action += ", then try Connect Microsoft Graph again"
	}
	if seen["AADSTS53003"] {
		action = "tenant Conditional Access blocked this sign-in; ask your administrator which browser sign-in policy must be satisfied"
	}
	if seen["AADSTS65001"] || seen["AADSTS65004"] || strings.Contains(message, "consent_required") {
		action = "consent was not granted; obtain the requested delegated Microsoft Graph permissions from your tenant administrator"
	}
	if len(details) == 0 {
		return fmt.Errorf("%s failed; %s", operation, action)
	}
	return fmt.Errorf("%s: %s; %s", operation, strings.Join(details, "; "), action)
}
