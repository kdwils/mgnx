package gluetun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/kdwils/mgnx/logger"
)

func ReadIp(ctx context.Context, path string) (net.IP, error) {
	var ip net.IP
	if err := waitForFile(ctx, path); err != nil {
		return ip, fmt.Errorf("waiting for IP file: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ip, fmt.Errorf("read external IP file: %w", err)
	}

	ip = net.ParseIP(strings.TrimSpace(string(data)))
	if ip == nil {
		return ip, fmt.Errorf("invalid IP in %s", path)
	}

	return ip, nil
}

// ReadForwardedPort waits for the forwarded port file to appear, reads it,
// and returns the parsed port number. The caller controls the deadline via ctx.
func ReadForwardedPort(ctx context.Context, path string) (int, error) {
	if err := waitForFile(ctx, path); err != nil {
		return 0, fmt.Errorf("waiting for port file: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read forwarded port file: %w", err)
	}

	p, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("invalid forwarded port in %s", path)
	}

	return p, nil
}

func waitForFile(ctx context.Context, path string) error {
	logger := logger.FromContext(ctx)
	for {
		_, err := os.Stat(path)
		if err == nil {
			return nil
		}

		logger.Warn("file not yet available", "path", path, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// WatchFiles watches the forwarded-port and external-IP files written by
// Gluetun. When either file changes it calls cancel so the server shuts down
// gracefully; Kubernetes then restarts the pod and picks up the new values.
// It is a no-op when both paths are empty.
func WatchFiles(ctx context.Context, cancel context.CancelFunc, portFile, ipFile string) error {
	if portFile == "" && ipFile == "" {
		return nil
	}

	log := logger.FromContext(ctx).With("service", "file watcher")

	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error("could not create watcher", "err", err)
		return err
	}
	defer w.Close()

	for _, path := range []string{portFile, ipFile} {
		if path == "" {
			continue
		}
		if err := w.Add(path); err != nil {
			log.Warn("could not add file", "path", path, "err", err)
		}
	}

	for {
		select {
		case event, ok := <-w.Events:
			if !ok {
				return errors.New("watcher event channel closed")
			}
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}
			log.Info("file changed, shutting down for restart", "file", event.Name)
			cancel()
			return nil
		case err, ok := <-w.Errors:
			if !ok {
				return errors.New("watcher error channel closed")
			}
			log.Warn("file watcher error", "err", err)
		case <-ctx.Done():
			return nil
		}
	}
}
