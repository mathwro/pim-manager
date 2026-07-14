package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	colorInk        = lipgloss.AdaptiveColor{Light: "#172033", Dark: "#EAF2FF"}
	colorMuted      = lipgloss.AdaptiveColor{Light: "#5F6B7A", Dark: "#8996A8"}
	colorAccent     = lipgloss.AdaptiveColor{Light: "#0067B8", Dark: "#67C7FF"}
	colorAccentSoft = lipgloss.AdaptiveColor{Light: "#E5F3FF", Dark: "#173B55"}
	colorViolet     = lipgloss.AdaptiveColor{Light: "#6750A4", Dark: "#B7A5FF"}
	colorSurface    = lipgloss.AdaptiveColor{Light: "#F5F8FC", Dark: "#111A29"}
	colorBorder     = lipgloss.AdaptiveColor{Light: "#CBD5E1", Dark: "#334155"}
	colorSuccess    = lipgloss.AdaptiveColor{Light: "#18794E", Dark: "#5EE2A0"}
	colorWarning    = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#F5C451"}
	colorDanger     = lipgloss.AdaptiveColor{Light: "#C42B1C", Dark: "#FF7B72"}

	appFrameStyle = lipgloss.NewStyle().
			Foreground(colorInk).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(1, 2)

	eyebrowStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true).
			Transform(strings.ToUpper)

	titleStyle = lipgloss.NewStyle().
			Foreground(colorInk).
			Bold(true).
			MarginTop(1)

	routeStyle   = lipgloss.NewStyle().Foreground(colorMuted)
	mutedStyle   = lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle  = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	violetStyle  = lipgloss.NewStyle().Foreground(colorViolet).Bold(true)
	spinnerStyle = lipgloss.NewStyle().Foreground(colorAccent)

	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colorBorder).
			Padding(0, 1)

	activeCardStyle = cardStyle.Copy().
			BorderForeground(colorAccent).
			Background(colorAccentSoft).
			Foreground(colorInk).
			Bold(true)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(1, 2)

	fieldStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	focusedFieldStyle = fieldStyle.Copy().BorderForeground(colorAccent)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorDanger).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colorDanger).
			PaddingLeft(1)

	keyStyle = lipgloss.NewStyle().
			Foreground(colorInk).
			Background(colorSurface).
			Padding(0, 1)

	footerStyle = lipgloss.NewStyle().Foreground(colorMuted).MarginTop(1)

	successStyle = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	warningStyle = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	dangerStyle  = lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
)
