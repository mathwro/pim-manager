package tui

import (
	"strings"

	"github.com/mathwro/pim-manager/internal/pim"
)

type assignmentList struct {
	items       []pim.EligibleAssignment
	selectedIDs map[string]bool
}

func newAssignmentList(items []pim.EligibleAssignment) assignmentList {
	return assignmentList{items: items, selectedIDs: map[string]bool{}}
}

func (l assignmentList) filtered(query string) []pim.EligibleAssignment {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return l.items
	}
	stateFilter := query == "active" || query == "inactive"
	wantActive := query == "active"
	var out []pim.EligibleAssignment
	for _, item := range l.items {
		if stateFilter {
			if item.Active == wantActive {
				out = append(out, item)
			}
			continue
		}
		haystack := strings.ToLower(item.DisplayName + " " + item.Scope.DisplayName + " " + string(item.Kind))
		if strings.Contains(haystack, query) {
			out = append(out, item)
		}
	}
	return out
}

func (l assignmentList) toggle(id string) {
	for _, item := range l.items {
		if item.ID != id {
			continue
		}
		if item.Active {
			delete(l.selectedIDs, id)
			return
		}
		l.selectedIDs[id] = !l.selectedIDs[id]
		return
	}
}

func (l assignmentList) selectedAssignments() []pim.EligibleAssignment {
	return l.selected()
}

func (l assignmentList) selected() []pim.EligibleAssignment {
	var out []pim.EligibleAssignment
	for _, item := range l.items {
		if l.selectedIDs[item.ID] {
			out = append(out, item)
		}
	}
	return out
}
