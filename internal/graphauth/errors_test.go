package graphauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	msalerrors "github.com/AzureAD/microsoft-authentication-library-for-go/apps/errors"
	"github.com/mathwro/pim-manager/internal/azureauth"
)

func TestMSALErrorRetainsOnlyAllowlistedDiagnostics(t *testing.T) {
	err := msalerrors.CallErr{
		Err:  errors.New("server response includes access_token=secret-token"),
		Resp: &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant","error_description":"AADSTS53003 blocked claims={secret-context}","access_token":"secret-token","claims":{"acrs":"secret-context"},"correlation_id":"55555555-5555-5555-5555-555555555555","trace_id":"66666666-6666-6666-6666-666666666666"}`))},
	}
	got := authenticationError(context.Background(), "connect Graph", err).Error()
	for _, required := range []string{"HTTP 400", "AADSTS53003", "invalid_grant", "55555555-5555-5555-5555-555555555555", "66666666-6666-6666-6666-666666666666", "Conditional Access"} {
		if !strings.Contains(got, required) {
			t.Fatalf("missing diagnostic %q: %s", required, got)
		}
	}
	for _, secret := range []string{"secret", "access_token", "claims", "acrs"} {
		if strings.Contains(got, secret) {
			t.Fatalf("leaked sensitive error content %q: %s", secret, got)
		}
	}
}

func TestAuthenticationErrorPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := authenticationError(ctx, "connect Graph", errors.New("secret response")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestARMLoginFailurePreservesActionableLoginCommand(t *testing.T) {
	got := authenticationError(tenantContext(), "check Azure CLI ARM identity", azureauth.ErrNotLoggedIn).Error()
	if !strings.Contains(got, "az login --tenant "+testTenant) {
		t.Fatalf("Azure CLI recovery command lost: %s", got)
	}
}
