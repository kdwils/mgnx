package cmd

import (
	"fmt"
	"log"

	"net"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/dht"
	"github.com/kdwils/mgnx/indexer"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/metadata"
	"github.com/kdwils/mgnx/scrape"
	"github.com/kdwils/mgnx/server"
	"github.com/kdwils/mgnx/service"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the mgnx server",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.New(viper.GetViper())
		if err != nil {
			log.Fatal(err)
		}

		l := logger.New(cfg.Server.LogLevel)

		ctx := logger.WithContext(cmd.Context(), l)

		pool, err := db.Connect(ctx, cfg.Database.URI)
		if err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		defer pool.Close()

		if err := db.RunMigrations(pool); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		queries := gen.New(pool)

		crawler, err := dht.NewCrawler(cfg.DHT)
		if err != nil {
			return fmt.Errorf("dht crawler: %w", err)
		}

		if err := crawler.Start(ctx); err != nil {
			return fmt.Errorf("crawler start: %w", err)
		}
		defer crawler.Stop()

		metaClient := metadata.NewClient(&net.Dialer{})
		idxWorker := indexer.New(crawler, metaClient, queries, cfg.Indexer)
		go idxWorker.Run(ctx)

		scrapeClient, err := scrape.NewClient(cfg.Scrape.Trackers[0], cfg.Scrape.DialTimeout, cfg.Scrape.ReadTimeout)
		if err != nil {
			return fmt.Errorf("scrape client: %w", err)
		}

		scrapeWorker := scrape.New(queries, scrapeClient, cfg.Scrape)
		go scrapeWorker.Run(ctx)

		svc := service.New(queries, cfg)
		srv := server.New(cfg.Server.Port, l, svc)
		return srv.Serve(ctx)
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
