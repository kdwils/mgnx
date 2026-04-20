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
	return readSettled(ctx, path, "IP", settleTime, func(s string) (net.IP, bool) {
		ip := net.ParseIP(s)
		return ip, ip != nil
	})
}

// ReadForwardedPort waits for a valid port number to appear in path, then
// blocks for settleTime with no further file changes before returning. Pass
// settleTime=0 to return as soon as a valid port is found.
func ReadForwardedPort(ctx context.Context, path string, settleTime time.Duration) (int, error) {
	return readSettled(ctx, path, "port", settleTime, func(s string) (int, bool) {
		p, err := strconv.Atoi(s)
		if err != nil || p <= 0 || p > 65535 {
			return 0, false
		}
		return p, true
	})
}

// readSettled is the shared implementation for ReadIp and ReadForwardedPort.
// It waits for path to exist, registers an fsnotify watch before reading
// (eliminating the TOCTOU window), parses the content with parseFn, and
// — when settleTime > 0 — waits for the file to be stable before returning.
func readSettled[T any](ctx context.Context, path, name string, settleTime time.Duration, parseFn func(string) (T, bool)) (T, error) {
	log := logger.FromContext(ctx)
	var zero T

	for {
		if err := waitForFile(ctx, path); err != nil {
			return zero, fmt.Errorf("waiting for %s file: %w", name, err)
		}

		// Register the watch BEFORE reading so no write between read and w.Add
		// can go undetected during the settle window.
		w, err := fsnotify.NewWatcher()
		if err != nil {
			return zero, fmt.Errorf("create %s watcher: %w", name, err)
		}

		if err := w.Add(path); err != nil {
			w.Close()
			return zero, fmt.Errorf("watch %s file: %w", name, err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			w.Close()
			log.Warn("failed to read file", "name", name, "path", path, "err", err)
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(1 * time.Second):
				continue
			}
		}

		value, ok := parseFn(strings.TrimSpace(string(data)))
		if !ok {
			w.Close()
			log.Warn("invalid content in file", "name", name, "path", path, "data", string(data))
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(1 * time.Second):
				continue
			}
		}

		if settleTime <= 0 {
			w.Close()
			return value, nil
		}

		settled, err := waitForSettle(ctx, w, path, settleTime)
		w.Close()
		if err != nil {
			return zero, err
		}
		if !settled {
			log.Info("file changed during settle window, re-reading", "name", name, "path", path)
			continue
		}

		return value, nil
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

// waitForSettle waits on an already-registered watcher for settleTime with no
// Write or Create events. Returns true if the file was stable for the full
// settle period, false if a change was detected (caller should re-read), or an
// error if ctx expires or the watcher channels close unexpectedly.
func waitForSettle(ctx context.Context, w *fsnotify.Watcher, path string, settleTime time.Duration) (bool, error) {
	log := logger.FromContext(ctx)

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
		data, err := os.ReadFile(ipFile)
		if err != nil {
			log.Warn("could not re-read IP file for drift check", "file", ipFile, "err", err)
		}
		if err == nil {
			current := net.ParseIP(strings.TrimSpace(string(data)))
			if current == nil || !current.Equal(settledIP) {
				log.Info("IP changed since settle, shutting down for restart", "file", ipFile, "settled", settledIP, "current", current)
				cancel()
				return nil
			}
		}
	}
	if portFile != "" && settledPort != 0 {
		data, err := os.ReadFile(portFile)
		if err != nil {
			log.Warn("could not re-read port file for drift check", "file", portFile, "err", err)
		}
		if err == nil {
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
