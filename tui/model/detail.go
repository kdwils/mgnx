package model

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kdwils/mgnx/pkg/client"
	"github.com/kdwils/mgnx/tui/ui"
)

type DetailModel struct {
	torrent      *client.Torrent
	loading      bool
	err          string
	pickerOpen   bool
	pickerCursor int
	client       *client.Client
	width        int
	height       int
}

func NewDetailModel(c *client.Client) DetailModel {
	return DetailModel{client: c}
}

func (m DetailModel) SetSize(w, h int) DetailModel {
	m.width = w
	m.height = h
	return m
}

func (m DetailModel) SetLoading(infohash string) DetailModel {
	m.loading = true
	m.err = ""
	m.torrent = nil
	m.pickerOpen = false
	return m
}

func (m DetailModel) Update(msg tea.Msg) (DetailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case TorrentLoadedMsg:
		t := msg.Torrent
		m.torrent = &t
		m.loading = false
		m.err = ""
		for i, s := range client.ValidStates {
			if s == t.State {
				m.pickerCursor = i
				break
			}
		}
		return m, nil

	case ErrMsg:
		m.loading = false
		m.err = msg.Err.Error()
		return m, nil

	case StateUpdatedMsg:
		if m.torrent != nil && m.torrent.Infohash == msg.Infohash {
			m.torrent.State = msg.NewState
		}
		m.pickerOpen = false
		return m, nil

	case tea.KeyMsg:
		if m.pickerOpen {
			return m.updatePicker(msg)
		}
		return m.updateMain(msg)
	}
	return m, nil
}

func (m DetailModel) updatePicker(msg tea.KeyMsg) (DetailModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.pickerCursor > 0 {
			m.pickerCursor--
		}
	case "down", "j":
		if m.pickerCursor < len(client.ValidStates)-1 {
			m.pickerCursor++
		}
	case "enter":
		if m.torrent == nil {
			m.pickerOpen = false
			return m, nil
		}
		newState := client.ValidStates[m.pickerCursor]
		infohash := m.torrent.Infohash
		return m, updateStateCmd(m.client, infohash, newState)
	case "esc":
		m.pickerOpen = false
	}
	return m, nil
}

func (m DetailModel) updateMain(msg tea.KeyMsg) (DetailModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "left", "h":
		return m, func() tea.Msg { return NavigateListMsg{} }
	case "s":
		m.pickerOpen = true
	case "d":
		if m.torrent != nil {
			t := m.torrent
			return m, func() tea.Msg {
				return NavigateConfirmDeleteMsg{Infohash: t.Infohash, Name: t.Name, From: viewDetail}
			}
		}
	case "y":
		if m.torrent != nil {
			magnet := m.torrent.MagnetURL()
			return m, copyToClipboard(magnet)
		}
	}
	return m, nil
}

func (m DetailModel) View() string {
	if m.width == 0 {
		return "loading…"
	}

	title := "Detail"
	if m.torrent != nil {
		title = m.torrent.Name
	}
	header := ui.HeaderStyle.Width(m.width).Render("← Esc   " + truncate(title, m.width-12))

	var body string
	if m.loading {
		body = "  loading…"
	}
	if m.err != "" {
		body = ui.ErrorStyle.Render("  " + m.err)
	}
	if m.torrent != nil {
		body = m.renderFields()
	}

	statusBar := ui.StatusBarStyle.Width(m.width).Render("[s] state  [d] delete  [y] copy magnet  [Esc] back")

	return lipgloss.JoinVertical(lipgloss.Left, header, body, statusBar)
}

func (m DetailModel) renderFields() string {
	t := m.torrent
	label := ui.LabelStyle
	val := lipgloss.NewStyle()

	row := func(l, v string) string {
		return label.Render(l) + val.Render(v)
	}

	lines := []string{
		row("Infohash", t.Infohash),
		m.renderStateRow(),
		row("Content Type", t.ContentType),
		row("Quality", t.Quality),
		row("Encoding", t.Encoding),
		row("Dynamic Range", t.DynamicRange),
		row("Source", t.Source),
		row("Release Group", t.ReleaseGroup),
		row("Size", fmt.Sprintf("%s  (%d files)", formatSize(t.TotalSize), t.FileCount)),
		row("Seeders", fmt.Sprintf("%d", t.Seeders)),
		row("Leechers", fmt.Sprintf("%d", t.Leechers)),
		row("First Seen", t.FirstSeen.UTC().Format("2006-01-02 15:04 UTC")),
		"",
		row("Magnet", truncate(t.MagnetURL(), m.width-20)+"  [y] copy"),
	}

	return "\n" + strings.Join(lines, "\n") + "\n"
}

func (m DetailModel) renderStateRow() string {
	label := ui.LabelStyle.Render("State")
	if !m.pickerOpen {
		stateStr := ""
		if m.torrent != nil {
			stateStr = lipgloss.NewStyle().Foreground(ui.StateColor(m.torrent.State)).Render(m.torrent.State)
		}
		return label + stateStr + "  " + ui.StatusBarStyle.Render("[s] change")
	}

	var pickerLines []string
	for i, s := range client.ValidStates {
		cursor := "  "
		if i == m.pickerCursor {
			cursor = "> "
		}
		line := cursor + s
		style := lipgloss.NewStyle().Foreground(ui.ColorMuted).Render(line)
		if i == m.pickerCursor {
			style = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true).Render(line)
		}
		pickerLines = append(pickerLines, style)
	}
	picker := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(ui.ColorBorder).
		Padding(0, 1).
		Render(strings.Join(pickerLines, "\n"))
	return label + "\n" + picker
}

func updateStateCmd(c *client.Client, infohash, state string) tea.Cmd {
	return func() tea.Msg {
		if err := c.UpdateTorrentState(context.Background(), infohash, state); err != nil {
			return ErrMsg{err}
		}
		return StateUpdatedMsg{Infohash: infohash, NewState: state}
	}
}

func copyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("pbcopy")
		case "linux":
			cmd = exec.Command("xclip", "-selection", "clipboard")
		case "windows":
			cmd = exec.Command("clip")
		default:
			return nil
		}
		cmd.Stdin = strings.NewReader(text)
		_ = cmd.Run()
		return nil
	}
}
