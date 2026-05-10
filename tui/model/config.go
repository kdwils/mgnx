package model

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kdwils/mgnx/pkg/torznab"
	"github.com/kdwils/mgnx/tui/ui"
)

type ConfigModel struct {
	hostInput textinput.Model
	err       string
	loading   bool
	hasClient bool
	width     int
	height    int
}

func NewConfigModel() ConfigModel {
	host := textinput.New()
	host.Placeholder = "localhost:8080"
	host.Width = 20
	host.Focus()

	return ConfigModel{
		hostInput: host,
	}
}

func (m ConfigModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m ConfigModel) SetSize(w, h int) ConfigModel {
	m.width = w
	m.height = h
	return m
}

func (m ConfigModel) SetHasClient(v bool) ConfigModel {
	m.hasClient = v
	return m
}

func (m ConfigModel) Update(msg tea.Msg) (ConfigModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			if m.hasClient {
				return m, func() tea.Msg { return NavigateListMsg{} }
			}
			return m, nil

		case "enter":
			host := strings.TrimSpace(m.hostInput.Value())
			m.loading = true
			m.err = ""
			return m, connectCmd(host)
		}

	case HealthCheckMsg:
		m.loading = false
		if !msg.Ok {
			m.err = strings.Join(msg.Reasons, "; ")
			if m.err == "" {
				m.err = "connection failed"
			}
		}
		return m, nil

	case ErrMsg:
		m.loading = false
		m.err = msg.Err.Error()
		return m, nil
	}

	updated, cmd := m.hostInput.Update(msg)
	m.hostInput = updated
	return m, cmd
}

func (m ConfigModel) View() string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorBorder).
		Padding(1, 2).
		Width(32)

	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")).Render("Connect to MGNX")

	hostLabel := ui.LabelStyle.Render("Host")

	content := title + "\n\n" +
		hostLabel + "  " + m.hostInput.View()

	if m.err != "" {
		content += "\n\n" + ui.ErrorStyle.Render("⚠ "+m.err)
	}

	if m.loading {
		content += "\n\n" + ui.StatusBarStyle.Render("connecting…")
	}

	hint := ui.StatusBarStyle.Render("[Enter] connect")
	if m.hasClient {
		hint += "  " + ui.StatusBarStyle.Render("[Esc] cancel")
	}
	content += "\n\n" + hint

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box.Render(content))
}

func connectCmd(host string) tea.Cmd {
	return func() tea.Msg {
		c := torznab.New(host, nil)
		_, err := c.ListTorrents(context.Background(), torznab.ListParams{Limit: 1})
		if err != nil {
			return HealthCheckMsg{Ok: false, Reasons: []string{fmt.Sprintf("cannot reach %s: %v", host, err)}}
		}
		return ClientConnectedMsg{Client: c}
	}
}
