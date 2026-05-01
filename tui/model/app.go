package model

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kdwils/mgnx/pkg/torznab"
	"github.com/kdwils/mgnx/tui/ui"
)

type view int

const (
	viewConfig view = iota
	viewList
	viewDetail
	viewConfirmDelete
)

type NavigateListMsg struct{}
type NavigateDetailMsg struct{ Infohash string }
type NavigateConfigMsg struct{}
type NavigateConfirmDeleteMsg struct {
	Infohash string
	Name     string
	From     view
}
type ClientConnectedMsg struct{ Client *torznab.Client }

type TorrentsLoadedMsg struct{ Torrents []torznab.Torrent }
type TorrentLoadedMsg struct{ Torrent torznab.Torrent }
type StateUpdatedMsg struct {
	Infohash string
	NewState string
}
type TorrentDeletedMsg struct{ Infohash string }
type HealthCheckMsg struct {
	Ok      bool
	Reasons []string
}
type ErrMsg struct{ Err error }

type App struct {
	activeView view
	prevView   view
	config     ConfigModel
	list       ListModel
	detail     DetailModel
	confirm    ConfirmModel
	client     *torznab.Client
	width      int
	height     int
}

func NewApp(c *torznab.Client) App {
	cfg := NewConfigModel()
	if c != nil {
		cfg = cfg.SetHasClient(true)
	}
	a := App{
		client:  c,
		config:  cfg,
		list:    NewListModel(c),
		detail:  NewDetailModel(c),
		confirm: ConfirmModel{},
	}
	a.activeView = viewConfig
	if c != nil {
		a.activeView = viewList
	}
	return a
}

func (a App) Init() tea.Cmd {
	if a.client != nil {
		return fetchTorrents(a.client, a.list.listParams())
	}
	return a.config.Init()
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.config = a.config.SetSize(msg.Width, msg.Height)
		a.list = a.list.SetSize(msg.Width, msg.Height)
		a.detail = a.detail.SetSize(msg.Width, msg.Height)
		return a, nil

	case tea.KeyMsg:
		if (msg.String() == "q" || msg.String() == "ctrl+c") && !a.list.searchInput.Focused() {
			return a, tea.Quit
		}
		if msg.String() == "ctrl+c" && a.list.searchInput.Focused() {
			return a, tea.Quit
		}
		if msg.String() == "c" && a.activeView != viewConfig && !a.list.searchInput.Focused() {
			a.prevView = a.activeView
			a.activeView = viewConfig
			return a, a.config.Init()
		}

	case NavigateListMsg:
		a.activeView = viewList
		return a, fetchTorrents(a.list.client, a.list.listParams())

	case NavigateDetailMsg:
		a.prevView = a.activeView
		a.activeView = viewDetail
		a.detail = a.detail.SetLoading()
		return a, getTorrent(a.client, msg.Infohash)

	case NavigateConfigMsg:
		a.prevView = a.activeView
		a.activeView = viewConfig
		return a, a.config.Init()

	case NavigateConfirmDeleteMsg:
		a.prevView = msg.From
		a.activeView = viewConfirmDelete
		a.confirm = NewConfirmModel(msg.Infohash, msg.Name, msg.From, a.client)
		return a, nil

	case ClientConnectedMsg:
		a.client = msg.Client
		a.config = a.config.SetHasClient(true)
		a.list = NewListModel(msg.Client).SetSize(a.width, a.height)
		a.detail = NewDetailModel(msg.Client).SetSize(a.width, a.height)
		a.activeView = viewList
		return a, fetchTorrents(msg.Client, a.list.listParams())

	case TorrentDeletedMsg:
		a.activeView = viewList
		a.list = a.list.RemoveTorrent(msg.Infohash)
		return a, nil
	}

	switch a.activeView {
	case viewConfig:
		updated, cmd := a.config.Update(msg)
		a.config = updated
		return a, cmd

	case viewList:
		updated, cmd := a.list.Update(msg)
		a.list = updated
		return a, cmd

	case viewDetail:
		updated, cmd := a.detail.Update(msg)
		a.detail = updated
		return a, cmd

	case viewConfirmDelete:
		updated, cmd := a.confirm.Update(msg)
		a.confirm = updated
		return a, cmd
	}

	return a, nil
}

func (a App) View() string {
	if a.activeView == viewConfirmDelete {
		modal := ui.ModalStyle.Render(a.confirm.View())
		return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, modal)
	}

	switch a.activeView {
	case viewConfig:
		return a.config.View()
	case viewList:
		return a.list.View()
	case viewDetail:
		return a.detail.View()
	}
	return ""
}

func getTorrent(c *torznab.Client, infohash string) tea.Cmd {
	return func() tea.Msg {
		t, err := c.GetTorrent(context.Background(), infohash)
		if err != nil {
			return ErrMsg{err}
		}
		return TorrentLoadedMsg{Torrent: *t}
	}
}
