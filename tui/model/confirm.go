package model

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kdwils/mgnx/pkg/client"
	"github.com/kdwils/mgnx/tui/ui"
)

type ConfirmModel struct {
	infohash string
	name     string
	fromView view
	client   *client.Client
	err      string
}

func NewConfirmModel(infohash, name string, from view, c *client.Client) ConfirmModel {
	return ConfirmModel{
		infohash: infohash,
		name:     name,
		fromView: from,
		client:   c,
	}
}

func (m ConfirmModel) Update(msg tea.Msg) (ConfirmModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			infohash := m.infohash
			return m, deleteCmd(m.client, infohash)
		case "esc", "n":
			from := m.fromView
			infohash := m.infohash
			return m, func() tea.Msg {
				if from == viewDetail {
					return NavigateDetailMsg{Infohash: infohash}
				}
				return NavigateListMsg{}
			}
		}
	case ErrMsg:
		m.err = msg.Err.Error()
		return m, nil
	}
	return m, nil
}

func (m ConfirmModel) View() string {
	name := truncate(m.name, 36)
	styledName := lipgloss.NewStyle().Bold(true).Render(name)

	var errLine string
	if m.err != "" {
		errLine = "\n\n" + ui.ErrorStyle.Render(m.err)
	}

	content := lipgloss.NewStyle().Bold(true).Render("Delete torrent?") + "\n\n" +
		styledName + "\n\n" +
		ui.StatusBarStyle.Render("This cannot be undone.") +
		errLine + "\n\n" +
		lipgloss.NewStyle().Bold(true).Render("[Enter] confirm") + "      " +
		ui.StatusBarStyle.Render("[Esc] cancel")

	return content
}

func deleteCmd(c *client.Client, infohash string) tea.Cmd {
	return func() tea.Msg {
		if err := c.DeleteTorrent(context.Background(), infohash); err != nil {
			return ErrMsg{err}
		}
		return TorrentDeletedMsg{Infohash: infohash}
	}
}
