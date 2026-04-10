package gluetun_test

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/kdwils/mgnx/gluetun"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "gluetun-test-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

func TestReadIp(t *testing.T) {
	t.Run("valid content returns ip immediately", func(t *testing.T) {
		path := writeTempFile(t, "1.2.3.4\n")
		ip, err := gluetun.ReadIp(context.Background(), path, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ip.String() != "1.2.3.4" {
			t.Errorf("got %s, want 1.2.3.4", ip)
		}
	})

	t.Run("invalid content is retried until valid", func(t *testing.T) {
		path := writeTempFile(t, "not-an-ip")

		go func() {
			time.Sleep(300 * time.Millisecond)
			os.WriteFile(path, []byte("5.6.7.8"), 0600)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		ip, err := gluetun.ReadIp(ctx, path, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ip.String() != "5.6.7.8" {
			t.Errorf("got %s, want 5.6.7.8", ip)
		}
	})

	t.Run("context cancellation stops retry on invalid content", func(t *testing.T) {
		path := writeTempFile(t, "not-an-ip")

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		_, err := gluetun.ReadIp(ctx, path, 0)
		if err == nil {
			t.Fatal("expected error on context cancellation, got nil")
		}
	})

	t.Run("blocks until file is stable for settle time", func(t *testing.T) {
		path := writeTempFile(t, "1.2.3.4")

		const settleTime = 400 * time.Millisecond

		// Overwrite before settle elapses; ReadIp should restart the settle window.
		go func() {
			time.Sleep(settleTime / 2)
			os.WriteFile(path, []byte("5.6.7.8"), 0600)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		start := time.Now()
		ip, err := gluetun.ReadIp(ctx, path, settleTime)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ip.String() != "5.6.7.8" {
			t.Errorf("got %s, want 5.6.7.8", ip)
		}
		minExpected := settleTime/2 + settleTime
		if elapsed < minExpected {
			t.Errorf("returned too early: elapsed %v, want >= %v", elapsed, minExpected)
		}
	})
}

func TestReadForwardedPort(t *testing.T) {
	t.Run("valid content returns port immediately", func(t *testing.T) {
		path := writeTempFile(t, "51820\n")
		port, err := gluetun.ReadForwardedPort(context.Background(), path, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if port != 51820 {
			t.Errorf("got %d, want 51820", port)
		}
	})

	t.Run("invalid content is retried until valid", func(t *testing.T) {
		path := writeTempFile(t, "not-a-port")

		go func() {
			time.Sleep(300 * time.Millisecond)
			os.WriteFile(path, []byte("51820"), 0600)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		port, err := gluetun.ReadForwardedPort(ctx, path, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if port != 51820 {
			t.Errorf("got %d, want 51820", port)
		}
	})

	t.Run("context cancellation stops retry on invalid content", func(t *testing.T) {
		path := writeTempFile(t, "not-a-port")

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		_, err := gluetun.ReadForwardedPort(ctx, path, 0)
		if err == nil {
			t.Fatal("expected error on context cancellation, got nil")
		}
	})

	t.Run("blocks until file is stable for settle time", func(t *testing.T) {
		path := writeTempFile(t, "51820")

		const settleTime = 400 * time.Millisecond

		go func() {
			time.Sleep(settleTime / 2)
			os.WriteFile(path, []byte("51821"), 0600)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		start := time.Now()
		port, err := gluetun.ReadForwardedPort(ctx, path, settleTime)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if port != 51821 {
			t.Errorf("got %d, want 51821", port)
		}
		minExpected := settleTime/2 + settleTime
		if elapsed < minExpected {
			t.Errorf("returned too early: elapsed %v, want >= %v", elapsed, minExpected)
		}
	})
}

func TestWatchFiles(t *testing.T) {
	t.Run("no-op when both paths are empty", func(t *testing.T) {
		called := false
		err := gluetun.WatchFiles(context.Background(), func() { called = true }, "", "", 0, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if called {
			t.Fatal("cancel should not have been called")
		}
	})

	t.Run("cancel called immediately on file change", func(t *testing.T) {
		path := writeTempFile(t, "1.2.3.4")

		ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ctxCancel()

		cancelCalled := make(chan struct{})
		cancel := func() { close(cancelCalled) }

		go func() {
			gluetun.WatchFiles(ctx, cancel, "", path, 0, net.ParseIP("1.2.3.4"))
		}()

		time.Sleep(50 * time.Millisecond)
		os.WriteFile(path, []byte("5.6.7.8"), 0600)

		select {
		case <-cancelCalled:
		case <-time.After(2 * time.Second):
			t.Fatal("cancel not called after file change")
		}
	})

	t.Run("cancel called immediately when ip changed since settle", func(t *testing.T) {
		// Write a different IP than the settled value — WatchFiles should fire
		// cancel without waiting for a future fsnotify event.
		path := writeTempFile(t, "5.6.7.8")

		ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ctxCancel()

		cancelCalled := make(chan struct{})
		cancel := func() { close(cancelCalled) }

		go func() {
			gluetun.WatchFiles(ctx, cancel, "", path, 0, net.ParseIP("1.2.3.4"))
		}()

		select {
		case <-cancelCalled:
		case <-time.After(2 * time.Second):
			t.Fatal("cancel not called after stale IP detected")
		}
	})

	t.Run("cancel called immediately when port changed since settle", func(t *testing.T) {
		path := writeTempFile(t, "51821")

		ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ctxCancel()

		cancelCalled := make(chan struct{})
		cancel := func() { close(cancelCalled) }

		go func() {
			gluetun.WatchFiles(ctx, cancel, path, "", 51820, nil)
		}()

		select {
		case <-cancelCalled:
		case <-time.After(2 * time.Second):
			t.Fatal("cancel not called after stale port detected")
		}
	})

	t.Run("context cancellation stops watcher without calling cancel", func(t *testing.T) {
		path := writeTempFile(t, "1.2.3.4")

		ctx, ctxCancel := context.WithCancel(context.Background())

		cancelCalled := make(chan struct{}, 1)
		cancel := func() { cancelCalled <- struct{}{} }

		done := make(chan struct{})
		go func() {
			defer close(done)
			gluetun.WatchFiles(ctx, cancel, "", path, 0, net.ParseIP("1.2.3.4"))
		}()

		time.Sleep(50 * time.Millisecond)
		ctxCancel()
		<-done

		select {
		case <-cancelCalled:
			t.Fatal("cancel was called despite context cancellation")
		default:
		}
	})
}
