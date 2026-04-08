package cmd

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
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
	"golang.org/x/sync/errgroup"
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

		pool, err := db.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return fmt.Errorf("db connect: %w", err)
		}

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

		metaClient := metadata.NewClient(
			metadata.TimeoutDialer{
				Dialer:      &net.Dialer{KeepAlive: -1},
				DialTimeout: 3 * time.Second,
			},
			cfg.DHT.MaxMessageSize,
			cfg.DHT.MaxMetadataSize,
		)

		crawler, err := dht.NewCrawler(cfg.DHT, cfg.Crawler)
		if err != nil {
			return fmt.Errorf("dht crawler: %w", err)
		}

		if err := db.RunMigrations(pool); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		queries := gen.New(pool)

		g, ctx := errgroup.WithContext(ctx)

		g.Go(func() error {
			err := gluetun.WatchFiles(ctx, cancel, cfg.DHT.ForwardedPortFile, cfg.DHT.ExternalIPFile)
			l.Info("gluetun watcher done", "error", err)
			if err != nil {
				logger.FromContext(ctx).Error("file watcher error", "error", err)
			}
			return err
		})

		g.Go(func() error {
			if err := crawler.Start(ctx); err != nil && err != context.Canceled {
				logger.FromContext(ctx).Error("crawler start error", "error", err)
				return err
			}
			l.Info("crawler started")
			<-ctx.Done()
			l.Info("crawler context done, stopping")
			err := crawler.Stop(ctx)
			l.Info("crawler stopped", "error", err)
			return nil
		})

		idxWorker := indexer.New(crawler, metaClient, queries, cfg.Indexer)
		g.Go(func() error {
			idxWorker.Run(ctx)
			l.Info("indexer stopped")
			return nil
		})

		scrapeClient, err := scrape.NewClient(cfg.Scrape.DialTimeout, cfg.Scrape.ReadTimeout, cfg.Scrape.Trackers...)
		if err != nil {
			return fmt.Errorf("scrape client: %w", err)
		}
		scrapeWorker := scrape.New(queries, scrapeClient, cfg.Scrape)
		g.Go(func() error {
			scrapeWorker.Run(ctx)
			l.Info("scrape worker stopped")
			return nil
		})

		g.Go(func() error {
			svc := service.New(queries, cfg)
			srv := torznab.New(cfg.Server.Port, l, svc)
			l.Info("starting torznab server")
			err := srv.Serve(ctx)
			l.Info("torznab server done", "error", err)
			if err != nil && err != context.Canceled {
				logger.FromContext(ctx).Error("torznab server error", "error", err)
			}
			return err
		})

		signalChan := make(chan os.Signal, 1)
		signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
		go func(ctx context.Context) {
			<-signalChan
			l.Warn("shutdown signal received, calling cancel()")
			cancel()
			l.Warn("cancel() called")
		}(ctx)

		l.Info("all goroutines started")
		err = g.Wait()
		signal.Stop(signalChan)
		l.Info("returning", "error", err)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
