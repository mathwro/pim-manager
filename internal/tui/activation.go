package tui

import (
	"fmt"
	"strings"

	"github.com/mathwro/pim-manager/internal/pim"
)

type activationForm struct {
	justification string
	durations     map[string]string
}

func (f activationForm) requiredJustifications(selected []pim.EligibleAssignment) int {
	count := 0
	for _, assignment := range selected {
		if assignment.ActivationPolicy.JustificationRequired {
			count++
		}
	}
	return count
}

func (f activationForm) validate(selected []pim.EligibleAssignment) error {
	required := f.requiredJustifications(selected)
	if required > 0 && strings.TrimSpace(f.justification) == "" {
		return fmt.Errorf("justification is required by %d selected assignment(s)", required)
	}
	for _, assignment := range selected {
		if strings.TrimSpace(f.durations[assignment.ID]) == "" {
			return fmt.Errorf("duration is required for %s", assignment.DisplayName)
		}
	}
	return nil
}

type summary struct {
	results             []pim.ActivationResult
	activated           []pim.ActivationResult
	pendingApproval     []pim.ActivationResult
	pendingProvisioning []pim.ActivationResult
	failed              []pim.ActivationResult
	justification       string
	durations           map[string]string
}

func newSummary(results []pim.ActivationResult) summary {
	return newSummaryWithInputs(results, "", nil)
}

func newSummaryWithInputs(results []pim.ActivationResult, justification string, durations map[string]string) summary {
	s := summary{
		justification: strings.TrimSpace(justification),
	}
	if len(durations) > 0 {
		s.durations = make(map[string]string, len(durations))
		for id, duration := range durations {
			s.durations[id] = strings.TrimSpace(duration)
		}
	}
	for _, result := range results {
		s.results = append(s.results, result)
		switch {
		case result.Success():
			s.activated = append(s.activated, result)
		case result.PendingApproval():
			s.pendingApproval = append(s.pendingApproval, result)
		case result.PendingProvisioning():
			s.pendingProvisioning = append(s.pendingProvisioning, result)
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
