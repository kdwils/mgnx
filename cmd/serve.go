package cmd

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/dht"
	"github.com/kdwils/mgnx/gluetun"
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

		ctx, cancel := context.WithCancel(logger.WithContext(cmd.Context(), l))
		defer cancel()

		go gluetun.WatchFiles(ctx, cancel, cfg.DHT.ForwardedPortFile, cfg.DHT.ExternalIPFile)

		if cfg.DHT.ExternalIPFile != "" && cfg.DHT.NodeID != "" {
			return fmt.Errorf("dht.external_ip_file and dht.node_id are mutually exclusive")
		}

		if cfg.DHT.ExternalIPFile != "" {
			data, err := os.ReadFile(cfg.DHT.ExternalIPFile)
			if err != nil {
				return fmt.Errorf("gluetun: read external IP file: %w", err)
			}
			ip := net.ParseIP(strings.TrimSpace(string(data)))
			if ip == nil {
				return fmt.Errorf("gluetun: invalid IP in %s", cfg.DHT.ExternalIPFile)
			}
			id, err := dht.DeriveNodeIDFromIP(ip)
			if err != nil {
				return fmt.Errorf("gluetun: derive node ID: %w", err)
			}
			cfg.DHT.NodeID = id.String()
			l.Info("gluetun: derived BEP-42 node ID from public IP", "ip", ip.String(), "node_id", cfg.DHT.NodeID)
		}

		if cfg.DHT.ForwardedPortFile != "" {
			data, err := os.ReadFile(cfg.DHT.ForwardedPortFile)
			if err != nil {
				return fmt.Errorf("gluetun: read forwarded port file: %w", err)
			}
			p, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || p <= 0 || p > 65535 {
				return fmt.Errorf("gluetun: invalid forwarded port in %s", cfg.DHT.ForwardedPortFile)
			}
			cfg.DHT.Port = p
			l.Info("gluetun: using forwarded port", "port", p)
		}

		pool, err := db.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		defer pool.Close()

		if err := db.RunMigrations(pool); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
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
		if err := srv.Serve(ctx); err != nil {
			return err
		}

		return ctx.Err()
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
