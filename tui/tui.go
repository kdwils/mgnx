package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/kdwils/mgnx/pkg/torznab"
	"github.com/kdwils/mgnx/tui/model"
)

func Run(host string) error {
	var c *torznab.Client
	if host != "" {
		c = torznab.New(host, nil)
	}
	p := tea.NewProgram(model.NewApp(c), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
