package cmd

import (
	"fmt"
	"log"

	"net"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/dht"
	"github.com/kdwils/mgnx/indexer"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/metadata"
	"github.com/kdwils/mgnx/scrape"
	"github.com/kdwils/mgnx/service"
	"github.com/kdwils/mgnx/torznab"
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

		pool, err := db.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		defer pool.Close()

		if err := db.RunMigrations(pool); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		queries := gen.New(pool)

		crawler, err := dht.NewCrawler(cfg.DHT, cfg.Crawler)
		if err != nil {
			return fmt.Errorf("dht crawler: %w", err)
		}

		if err := crawler.Start(ctx); err != nil {
			return fmt.Errorf("crawler start: %w", err)
		}
		defer crawler.Stop(ctx)

		metaClient := metadata.NewClient(
			metadata.TimeoutDialer{
				Dialer:      &net.Dialer{KeepAlive: -1},
				DialTimeout: 3 * time.Second,
			},
			cfg.DHT.MaxMessageSize,
			cfg.DHT.MaxMetadataSize,
		)
		idxWorker := indexer.New(crawler, metaClient, queries, cfg.Indexer)
		go idxWorker.Run(ctx)

		scrapeClient, err := scrape.NewClient(cfg.Scrape.Trackers[0], cfg.Scrape.DialTimeout, cfg.Scrape.ReadTimeout)
		if err != nil {
			return fmt.Errorf("scrape client: %w", err)
		}

		scrapeWorker := scrape.New(queries, scrapeClient, cfg.Scrape)
		go scrapeWorker.Run(ctx)

		svc := service.New(queries, cfg)
		srv := torznab.New(cfg.Server.Port, l, svc)
		return srv.Serve(ctx)
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
