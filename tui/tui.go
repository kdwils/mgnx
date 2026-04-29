package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/kdwils/mgnx/pkg/client"
	"github.com/kdwils/mgnx/tui/model"
)

// Run starts the bubbletea program.
// If host is empty, the app opens the Config view first.
// If host is provided, it skips straight to the List view.
func Run(host string) error {
	c := client.New(host, nil)
	p := tea.NewProgram(model.NewApp(c), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
