package tui

import (
	"strings"

	"github.com/mathwro/pim-manager/internal/pim"
)

type activationForm struct {
	justification string
	durationISO   string
}

func (f activationForm) valid() bool {
	return strings.TrimSpace(f.justification) != "" && strings.TrimSpace(f.durationISO) != ""
}

type summary struct {
	activated       []pim.ActivationResult
	pendingApproval []pim.ActivationResult
	failed          []pim.ActivationResult
}

func newSummary(results []pim.ActivationResult) summary {
	var s summary
	for _, result := range results {
		switch {
		case result.Success():
			s.activated = append(s.activated, result)
		case result.PendingApproval():
			s.pendingApproval = append(s.pendingApproval, result)
		case result.Failure():
			s.failed = append(s.failed, result)
		}
	}
	return s
}

func (s summary) retryableFailures() []pim.ActivationResult {
	var out []pim.ActivationResult
	for _, result := range s.failed {
		if result.CanRetry() {
			out = append(out, result)
		}
	}
	return out
}
