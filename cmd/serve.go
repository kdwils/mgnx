package cmd

import (
	"context"
	"fmt"
	"log"

	"github.com/kdwils/magnetite/config"
	"github.com/kdwils/magnetite/db"
	"github.com/kdwils/magnetite/db/queries"
	"github.com/kdwils/magnetite/logger"
	"github.com/kdwils/magnetite/server"
	"github.com/kdwils/magnetite/service"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the magnetite server",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.New(viper.GetViper())
		if err != nil {
			log.Fatal(err)
		}

		logger := logger.New(cfg.Server.LogLevel)

		ctx := context.Background()

		pool, err := db.Connect(ctx, cfg.Database.DSN())
		if err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		defer pool.Close()

		if err := db.RunMigrations(pool); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		svc := service.New(queries.New(pool), cfg)

		server := server.New(cfg.Server.Port, logger, svc)

		return server.Serve(cmd.Context())
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
