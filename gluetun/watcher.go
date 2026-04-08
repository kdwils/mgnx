package gluetun

import (
	"context"
	"errors"

	"github.com/fsnotify/fsnotify"
	"github.com/kdwils/mgnx/logger"
)

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
