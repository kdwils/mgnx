package cmd

import (
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/tui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Interactive terminal UI for browsing and managing torrents",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.New(viper.GetViper())
		if err != nil {
			return err
		}
		return tui.Run(cmd.Context(), cfg.TUI.Host)
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
