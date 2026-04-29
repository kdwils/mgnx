package ui

import "github.com/charmbracelet/lipgloss"

var (
	ColorActive     = lipgloss.Color("46")
	ColorPending    = lipgloss.Color("220")
	ColorClassified = lipgloss.Color("39")
	ColorEnriched   = lipgloss.Color("63")
	ColorDead       = lipgloss.Color("196")
	ColorRejected   = lipgloss.Color("240")
	ColorMuted      = lipgloss.Color("245")
	ColorBorder     = lipgloss.Color("238")
	ColorSelected   = lipgloss.Color("237")
)

func StateColor(state string) lipgloss.Color {
	switch state {
	case "active":
		return ColorActive
	case "pending":
		return ColorPending
	case "classified":
		return ColorClassified
	case "enriched":
		return ColorEnriched
	case "dead":
		return ColorDead
	default:
		return ColorRejected
	}
}

var (
	HeaderStyle = lipgloss.NewStyle().
			Bold(true).Padding(0, 1).
			Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252"))

	StatusBarStyle = lipgloss.NewStyle().
			Padding(0, 1).
			Background(lipgloss.Color("235")).Foreground(ColorMuted)

	SelectedRowStyle = lipgloss.NewStyle().
				Background(ColorSelected).Bold(true)

	SidebarStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, true, false, false).
			BorderForeground(ColorBorder).
			Width(16).Padding(1, 1)

	ModalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("205")).
			Padding(1, 2)

	LabelStyle = lipgloss.NewStyle().
			Foreground(ColorMuted).Width(16)

	ErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))
)
