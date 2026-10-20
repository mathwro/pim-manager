package tui

import (
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const activityCapacity = 64

type activityEntry struct {
	at      time.Time
	message string
	failed  bool
}

func (m *Model) recordActivity(message string, failed bool) {
	entry := activityEntry{at: time.Now(), message: sanitizeActivityLabel(message), failed: failed}
	if len(m.activityEntries) == activityCapacity {
		copy(m.activityEntries, m.activityEntries[1:])
		m.activityEntries[len(m.activityEntries)-1] = entry
		return
	}
	m.activityEntries = append(m.activityEntries, entry)
}

func (m *Model) beginOperation(key string) {
	if m.operationStarted == nil {
		m.operationStarted = make(map[string]time.Time)
	}
	if _, started := m.operationStarted[key]; !started {
		m.operationStarted[key] = time.Now()
	}
}

func (m *Model) endOperation(key string) {
	delete(m.operationStarted, key)
}

func sanitizeActivityLabel(label string) string {
	label = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, ansi.Strip(label))
	return ansi.Truncate(strings.Join(strings.Fields(label), " "), 160, "…")
}
