package model

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kdwils/mgnx/pkg/torznab"
	"github.com/kdwils/mgnx/tui/ui"
)

const (
	pageSize    = 50
	sidebarW    = 20 // sidebar rendered width including border+padding
	stateColW   = 11
	typeColW    = 7
	seedersColW = 6
	sizeColW    = 7
	colSpacing  = 1
	fixedColW   = stateColW + typeColW + seedersColW + sizeColW + 4*colSpacing
)

var stateOptions = append([]string{"all"}, torznab.ValidStates...)
var typeOptions = []string{"all", "movie", "tv", "anime", "unknown"}

type ListModel struct {
	torrents    []torznab.Torrent
	cursor      int
	page        int
	isLastPage  bool
	stateFilter string
	typeFilter  string
	searchQuery string
	searchInput textinput.Model
	filterFocus int // 0=state, 1=type, 2=search
	loading     bool
	err         string
	spinner     spinner.Model
	client      *torznab.Client
	width       int
	height      int
}

func NewListModel(c *torznab.Client) ListModel {
	si := textinput.New()
	si.Placeholder = "search…"
	si.Width = 14

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return ListModel{
		client:      c,
		searchInput: si,
		spinner:     sp,
		stateFilter: "active",
	}
}

func (m ListModel) SetSize(w, h int) ListModel {
	m.width = w
	m.height = h
	return m
}

func (m ListModel) RemoveTorrent(infohash string) ListModel {
	var filtered []torznab.Torrent
	for _, t := range m.torrents {
		if t.Infohash != infohash {
			filtered = append(filtered, t)
		}
	}
	m.torrents = filtered
	if m.cursor >= len(m.torrents) && m.cursor > 0 {
		m.cursor = len(m.torrents) - 1
	}
	return m
}

func (m ListModel) listParams() torznab.ListParams {
	p := torznab.ListParams{
		Limit:  pageSize,
		Offset: m.page * pageSize,
	}
	if m.stateFilter != "" && m.stateFilter != "all" {
		p.State = m.stateFilter
	}
	if m.typeFilter != "" && m.typeFilter != "all" {
		p.ContentType = m.typeFilter
	}
	return p
}

func (m ListModel) visibleTorrents() []torznab.Torrent {
	if m.searchQuery == "" {
		return m.torrents
	}
	q := strings.ToLower(m.searchQuery)
	var out []torznab.Torrent
	for _, t := range m.torrents {
		if strings.Contains(strings.ToLower(t.Name), q) {
			out = append(out, t)
		}
	}
	return out
}

func (m ListModel) Update(msg tea.Msg) (ListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case TorrentsLoadedMsg:
		m.loading = false
		m.torrents = msg.Torrents
		m.isLastPage = len(msg.Torrents) < pageSize
		m.cursor = 0
		return m, nil

	case ErrMsg:
		m.loading = false
		m.err = msg.Err.Error()
		return m, nil

	case spinner.TickMsg:
		if m.loading {
			sp, cmd := m.spinner.Update(msg)
			m.spinner = sp
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		if m.searchInput.Focused() {
			return m.updateSearchFocused(msg)
		}
		return m.updateTableFocused(msg)
	}

	if m.searchInput.Focused() {
		updated, cmd := m.searchInput.Update(msg)
		m.searchInput = updated
		return m, cmd
	}
	return m, nil
}

func (m ListModel) updateSearchFocused(msg tea.KeyMsg) (ListModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searchQuery = ""
		m.searchInput.SetValue("")
		m.searchInput.Blur()
		return m, nil
	case "enter":
		m.searchQuery = m.searchInput.Value()
		m.searchInput.Blur()
		return m, nil
	}
	updated, cmd := m.searchInput.Update(msg)
	m.searchInput = updated
	return m, cmd
}

func (m ListModel) updateTableFocused(msg tea.KeyMsg) (ListModel, tea.Cmd) {
	visible := m.visibleTorrents()
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(visible)-1 {
			m.cursor++
		}
	case "left", "h":
		if m.page > 0 {
			m.page--
			m.loading = true
			m.err = ""
			return m, tea.Batch(m.spinner.Tick, fetchTorrents(m.client, m.listParams()))
		}
	case "right", "l":
		if !m.isLastPage {
			m.page++
			m.loading = true
			m.err = ""
			return m, tea.Batch(m.spinner.Tick, fetchTorrents(m.client, m.listParams()))
		}
	case "enter":
		if len(visible) > 0 && m.cursor < len(visible) {
			t := visible[m.cursor]
			return m, func() tea.Msg { return NavigateDetailMsg{Infohash: t.Infohash} }
		}
	case "/", "ctrl+f":
		m.searchInput.Focus()
		return m, textinput.Blink
	case "tab":
		m.filterFocus = (m.filterFocus + 1) % 3
		if m.filterFocus == 2 {
			m.searchInput.Focus()
			return m, textinput.Blink
		}
	case "s":
		m.stateFilter = nextOption(stateOptions, m.stateFilter)
		m.page = 0
		m.loading = true
		m.err = ""
		return m, tea.Batch(m.spinner.Tick, fetchTorrents(m.client, m.listParams()))
	case "t":
		m.typeFilter = nextOption(typeOptions, m.typeFilter)
		m.page = 0
		m.loading = true
		m.err = ""
		return m, tea.Batch(m.spinner.Tick, fetchTorrents(m.client, m.listParams()))
	case "d":
		if len(visible) > 0 && m.cursor < len(visible) {
			t := visible[m.cursor]
			return m, func() tea.Msg {
				return NavigateConfirmDeleteMsg{Infohash: t.Infohash, Name: t.Name, From: viewList}
			}
		}
	case "r":
		m.loading = true
		m.err = ""
		return m, tea.Batch(m.spinner.Tick, fetchTorrents(m.client, m.listParams()))
	case "c":
		return m, func() tea.Msg { return NavigateConfigMsg{} }
	}
	return m, nil
}

func (m ListModel) View() string {
	if m.width == 0 {
		return "loading…"
	}

	header := m.renderHeader()
	sidebar := m.renderSidebar()
	table := m.renderTable()
	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, table)
	statusBar := m.renderStatusBar()

	return lipgloss.JoinVertical(lipgloss.Left, header, body, statusBar)
}

func (m ListModel) renderHeader() string {
	title := "mgnx"
	if m.client != nil {
		title = "mgnx  •  ● connected"
	}
	if m.loading {
		title += "  " + m.spinner.View() + " loading"
	}
	return ui.HeaderStyle.Width(m.width).Render(title)
}

func (m ListModel) renderSidebar() string {
	sf := m.stateFilter
	if sf == "" {
		sf = "all"
	}
	tf := m.typeFilter
	if tf == "" {
		tf = "all"
	}

	stateLabel := ui.LabelStyle.Render("State")
	stateVal := fmt.Sprintf("[%s ▼]", sf)
	if m.filterFocus == 0 {
		stateVal = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true).Render(stateVal)
	}

	typeLabel := ui.LabelStyle.Render("Type")
	typeVal := fmt.Sprintf("[%s ▼]", tf)
	if m.filterFocus == 1 {
		typeVal = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true).Render(typeVal)
	}

	searchLabel := ui.LabelStyle.Render("Search")

	content := "Filters\n" +
		strings.Repeat("─", 14) + "\n\n" +
		stateLabel + "\n" + stateVal + "\n\n" +
		typeLabel + "\n" + typeVal + "\n\n" +
		searchLabel + "\n" + m.searchInput.View()

	tableH := max(m.height-3, 4)
	return ui.SidebarStyle.Height(tableH).Render(content)
}

func (m ListModel) renderTable() string {
	tableWidth := max(m.width-sidebarW, 20)
	nameWidth := max(tableWidth-fixedColW-2, 10)

	tableH := max(m.height-3, 4)
	maxRows := tableH - 1 // minus header row

	header := m.renderTableHeader(nameWidth, tableWidth)

	visible := m.visibleTorrents()
	var rows []string
	for i, t := range visible {
		if i >= maxRows {
			break
		}
		rows = append(rows, m.renderRow(t, i == m.cursor, nameWidth))
	}

	if m.err != "" {
		rows = append(rows, ui.ErrorStyle.Render(m.err))
	}

	content := header + "\n" + strings.Join(rows, "\n")
	return lipgloss.NewStyle().Width(tableWidth).Height(tableH).Render(content)
}

func (m ListModel) renderTableHeader(nameWidth, tableWidth int) string {
	h := lipgloss.NewStyle().Foreground(ui.ColorMuted).Bold(true)
	name := h.Width(nameWidth + colSpacing).Render("Name")
	state := h.Width(stateColW + colSpacing).Render("State")
	ctype := h.Width(typeColW + colSpacing).Render("Type")
	seeders := h.Width(seedersColW + colSpacing).Align(lipgloss.Right).Render("Seed")
	size := h.Width(sizeColW).Align(lipgloss.Right).Render("Size")
	return name + state + ctype + seeders + size
}

func (m ListModel) renderRow(t torznab.Torrent, selected bool, nameWidth int) string {
	applyBg := func(s lipgloss.Style) lipgloss.Style {
		if selected {
			return s.Background(ui.ColorSelected).Bold(true)
		}
		return s
	}

	icon := typeIcon(t.ContentType)
	rawName := icon + " " + t.Name
	name := applyBg(lipgloss.NewStyle().Width(nameWidth + colSpacing)).Render(truncate(rawName, nameWidth))
	state := applyBg(lipgloss.NewStyle().Width(stateColW + colSpacing).Foreground(ui.StateColor(t.State))).Render(t.State)
	ctype := applyBg(lipgloss.NewStyle().Width(typeColW + colSpacing).Foreground(ui.ColorMuted)).Render(t.ContentType)
	seeders := applyBg(lipgloss.NewStyle().Width(seedersColW + colSpacing).Align(lipgloss.Right)).Render(fmt.Sprintf("%d", t.Seeders))
	size := applyBg(lipgloss.NewStyle().Width(sizeColW).Align(lipgloss.Right).Foreground(ui.ColorMuted)).Render(formatSize(t.TotalSize))
	return name + state + ctype + seeders + size
}

func (m ListModel) renderStatusBar() string {
	visible := m.visibleTorrents()
	counts := fmt.Sprintf("%d torrents  •  page %d", len(visible), m.page+1)
	if !m.isLastPage {
		counts += "+"
	}

	keys1 := counts + "  •  ↑↓ move  ←→ page  enter open"
	keys2 := "/ ctrl+f search (page only)  s state  t type  d delete  r refresh  c config  q quit"

	line1 := ui.StatusBarStyle.Width(m.width).Render(keys1)
	line2 := ui.StatusBarStyle.Width(m.width).Render(keys2)
	return line1 + "\n" + line2
}

func fetchTorrents(c *torznab.Client, p torznab.ListParams) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return ErrMsg{fmt.Errorf("no client configured")}
		}
		torrents, err := c.ListTorrents(context.Background(), p)
		if err != nil {
			return ErrMsg{err}
		}
		return TorrentsLoadedMsg{Torrents: torrents}
	}
}

func nextOption(options []string, current string) string {
	if current == "" {
		current = "all"
	}
	for i, o := range options {
		if o == current {
			return options[(i+1)%len(options)]
		}
	}
	return options[0]
}

func typeIcon(contentType string) string {
	switch contentType {
	case "movie":
		return "[M]"
	case "tv":
		return "[T]"
	case "anime":
		return "[A]"
	default:
		return "[?]"
	}
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

func truncate(s string, maxWidth int) string {
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	if maxWidth < 1 {
		return ""
	}
	return string(runes[:maxWidth-1]) + "…"
}
