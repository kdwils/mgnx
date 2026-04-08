package gluetun

import (
	"context"

	"github.com/fsnotify/fsnotify"
	"github.com/kdwils/mgnx/logger"
)

// WatchFiles watches the forwarded-port and external-IP files written by
// Gluetun. When either file changes it calls cancel so the server shuts down
// gracefully; Kubernetes then restarts the pod and picks up the new values.
// It is a no-op when both paths are empty.
func WatchFiles(ctx context.Context, cancel context.CancelFunc, portFile, ipFile string) {
	if portFile == "" && ipFile == "" {
		return
	}

	log := logger.FromContext(ctx)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Warn("gluetun file watcher: could not create watcher", "err", err)
		return
	}
	defer w.Close()

	for _, path := range []string{portFile, ipFile} {
		if path == "" {
			continue
		}
		if err := w.Add(path); err != nil {
			log.Warn("gluetun file watcher: could not watch file", "path", path, "err", err)
		}
	}

	for {
		select {
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}
			log.Info("gluetun file changed, shutting down for restart", "file", event.Name)
			cancel()
			return
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Warn("gluetun file watcher error", "err", err)
		case <-ctx.Done():
			return
		}
	}
}
