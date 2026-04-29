package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/kdwils/mgnx/pkg/client"
	"github.com/kdwils/mgnx/tui/model"
)

func Run(host string) error {
	var c *client.Client
	if host != "" {
		c = client.New(host, nil)
	}
	p := tea.NewProgram(model.NewApp(c), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
