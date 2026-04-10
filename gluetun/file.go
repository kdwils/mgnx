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

// ReadIp waits for a valid IP address to appear in path, then blocks for
// settleTime with no further file changes before returning. Pass settleTime=0
// to return as soon as a valid IP is found.
func ReadIp(ctx context.Context, path string, settleTime time.Duration) (net.IP, error) {
	log := logger.FromContext(ctx)
	for {
		if err := waitForFile(ctx, path); err != nil {
			return nil, fmt.Errorf("waiting for IP file: %w", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read external IP file: %w", err)
		}

		ip := net.ParseIP(strings.TrimSpace(string(data)))
		if ip == nil {
			log.Warn("invalid IP in file, retrying", "path", path)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(1 * time.Second):
				continue
			}
		}

		if settleTime <= 0 {
			return ip, nil
		}

		settled, err := waitForSettle(ctx, path, settleTime)
		if err != nil {
			return nil, err
		}
		if !settled {
			log.Info("IP file changed during settle window, re-reading", "path", path)
			continue
		}

		return ip, nil
	}
}

// ReadForwardedPort waits for a valid port number to appear in path, then
// blocks for settleTime with no further file changes before returning. Pass
// settleTime=0 to return as soon as a valid port is found.
func ReadForwardedPort(ctx context.Context, path string, settleTime time.Duration) (int, error) {
	log := logger.FromContext(ctx)
	for {
		if err := waitForFile(ctx, path); err != nil {
			return 0, fmt.Errorf("waiting for port file: %w", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return 0, fmt.Errorf("read forwarded port file: %w", err)
		}

		p, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || p <= 0 || p > 65535 {
			log.Warn("invalid forwarded port in file, retrying", "path", path)
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(1 * time.Second):
				continue
			}
		}

		if settleTime <= 0 {
			return p, nil
		}

		settled, err := waitForSettle(ctx, path, settleTime)
		if err != nil {
			return 0, err
		}
		if !settled {
			log.Info("port file changed during settle window, re-reading", "path", path)
			continue
		}

		return p, nil
	}
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

// waitForSettle watches path for settleTime with no Write or Create events.
// Returns true if the file was stable for the full settle period, false if
// a change was detected (caller should re-read), or an error if ctx expires.
func waitForSettle(ctx context.Context, path string, settleTime time.Duration) (bool, error) {
	log := logger.FromContext(ctx)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return false, fmt.Errorf("create settle watcher: %w", err)
	}
	defer w.Close()

	if err := w.Add(path); err != nil {
		return false, fmt.Errorf("watch file for settle: %w", err)
	}

	log.Info("waiting for file to settle", "path", path, "settle_time", settleTime)
	timer := time.NewTimer(settleTime)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			return true, nil
		case event, ok := <-w.Events:
			if !ok {
				return false, errors.New("settle watcher event channel closed")
			}
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}
			log.Info("file changed during settle window", "path", path)
			return false, nil
		case err, ok := <-w.Errors:
			if !ok {
				return false, errors.New("settle watcher error channel closed")
			}
			log.Warn("settle watcher error", "err", err)
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// WatchFiles watches the forwarded-port and external-IP files written by
// Gluetun. After registering watches it immediately re-reads both files and
// compares them against the values that were settled on at startup; if either
// has already changed it calls cancel without waiting for a future event.
// On any subsequent file change it calls cancel so the server shuts down
// gracefully; Kubernetes then restarts the pod and picks up the new values.
// It is a no-op when both paths are empty.
func WatchFiles(ctx context.Context, cancel context.CancelFunc, portFile, ipFile string, settledPort int, settledIP net.IP) error {
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

	// Check for changes that slipped through between settle and watch registration.
	if ipFile != "" && settledIP != nil {
		if data, err := os.ReadFile(ipFile); err == nil {
			current := net.ParseIP(strings.TrimSpace(string(data)))
			if current == nil || !current.Equal(settledIP) {
				log.Info("IP changed since settle, shutting down for restart", "file", ipFile, "settled", settledIP, "current", current)
				cancel()
				return nil
			}
		}
	}
	if portFile != "" && settledPort != 0 {
		if data, err := os.ReadFile(portFile); err == nil {
			p, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || p != settledPort {
				log.Info("port changed since settle, shutting down for restart", "file", portFile, "settled", settledPort, "current", p)
				cancel()
				return nil
			}
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
