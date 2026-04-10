package cmd

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/dht"
	"github.com/kdwils/mgnx/gluetun"
	"github.com/kdwils/mgnx/health"
	"github.com/kdwils/mgnx/indexer"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/metadata"
	"github.com/kdwils/mgnx/metrics"
	"github.com/kdwils/mgnx/recorder"
	"github.com/kdwils/mgnx/scrape"
	"github.com/kdwils/mgnx/service"
	"github.com/kdwils/mgnx/torznab"
	"github.com/prometheus/client_golang/prometheus"
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

		g, ctx := errgroup.WithContext(ctx)
		if cfg.DHT.ExternalIPFile != "" || cfg.DHT.ForwardedPortFile != "" {
			g.Go(func() error {
				return gluetun.WatchFiles(ctx, cancel, cfg.DHT.ExternalIPFile, cfg.DHT.ForwardedPortFile)
			})
		}

		if cfg.DHT.ExternalIPFile != "" {
			fileCtx, fileCancel := context.WithTimeout(ctx, cfg.DHT.FileWaitTimeout)
			ip, err := gluetun.ReadIp(fileCtx, cfg.DHT.ExternalIPFile)
			fileCancel()
			if err != nil {
				return err
			}

			nodeID, err := dht.DeriveNodeIDFromIP(ip)
			if err != nil {
				return fmt.Errorf("failed to derive node ID from ip address: %w", err)
			}

			cfg.DHT.NodeID = nodeID.String()
			l.Info("derived node ID from ip", "id", nodeID.String())
		}

		if cfg.DHT.ForwardedPortFile != "" {
			fileCtx, fileCancel := context.WithTimeout(ctx, cfg.DHT.FileWaitTimeout)
			port, err := gluetun.ReadForwardedPort(fileCtx, cfg.DHT.ForwardedPortFile)
			fileCancel()
			if err != nil {
				return err
			}
			cfg.DHT.Port = port
			l.Info("using forwarded port from file", "port", port)
		}

		pool, err := db.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		queries := gen.New(pool)

		reg := prometheus.NewPedanticRegistry()
		rec, err := recorder.New(reg)
		if err != nil {
			return fmt.Errorf("create recorder: %w", err)
		}

		crawler, err := dht.NewCrawler(cfg.Crawler, cfg.DHT, rec)
		if err != nil {
			return fmt.Errorf("dht crawler: %w", err)
		}

		scrapeClient, err := scrape.NewClient(cfg.Scrape.DialTimeout, cfg.Scrape.ReadTimeout, cfg.Scrape.Trackers...)
		if err != nil {
			return fmt.Errorf("scrape client: %w", err)
		}
		scrapeWorker := scrape.New(queries, scrapeClient, cfg.Scrape, rec)

		hs := health.New(cfg.Server.HealthPort, pool, crawler)
		g.Go(func() error {
			return hs.Start(ctx)
		})

		ms := metrics.New(cfg.Server.MetricsPort, reg)
		g.Go(func() error {
			return ms.Start(ctx)
		})

		if err := db.RunMigrations(pool); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		metaClient := metadata.NewClient(
			metadata.TimeoutDialer{
				Dialer:      &net.Dialer{KeepAlive: -1},
				DialTimeout: 3 * time.Second,
			},
			cfg.DHT.MaxMessageSize,
			cfg.DHT.MaxMetadataSize,
			rec,
		)

		g.Go(func() error {
			err := gluetun.WatchFiles(ctx, cancel, cfg.DHT.ForwardedPortFile, cfg.DHT.ExternalIPFile)
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
			<-ctx.Done()
			err := crawler.Stop(ctx)
			return err
		})
		idxWorker := indexer.New(crawler, metaClient, queries, cfg.Indexer, rec)
		g.Go(func() error {
			idxWorker.Run(ctx)
			return nil
		})
		g.Go(func() error {
			scrapeWorker.Run(ctx)
			return nil
		})
		g.Go(func() error {
			svc := service.New(queries, cfg)
			srv := torznab.New(cfg.Server.TorznabPort, l, svc, rec)
			err := srv.Serve(ctx)
			if err != nil && err != context.Canceled {
				logger.FromContext(ctx).Error("torznab server error", "error", err)
			}
			return err
		})

		signalChan := make(chan os.Signal, 1)
		signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
		go func(ctx context.Context) {
			<-signalChan
			cancel()
		}(ctx)

		l.Info("all services started")
		err = g.Wait()
		signal.Stop(signalChan)
		l.Info("all services stopped")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
