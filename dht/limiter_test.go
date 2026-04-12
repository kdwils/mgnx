package dht

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIPLimiter(t *testing.T) {
	t.Run("allows within burst", func(t *testing.T) {
		limiter := newIPLimiter(10, 10, time.Minute, 0)

		ip := net.ParseIP("192.168.1.1")

		for i := range 10 {
			assert.True(t, limiter.Allow(ip), "expected allow for request %d", i+1)
		}
	})

	t.Run("denies after burst exceeded", func(t *testing.T) {
		limiter := newIPLimiter(10, 10, time.Minute, 0)

		ip := net.ParseIP("192.168.1.1")

		for range 10 {
			limiter.Allow(ip)
		}

		assert.False(t, limiter.Allow(ip), "expected deny after burst exceeded")
	})

	t.Run("each IP has independent limit", func(t *testing.T) {
		limiter := newIPLimiter(2, 2, time.Minute, 0)

		ip1 := net.ParseIP("192.168.1.1")
		ip2 := net.ParseIP("192.168.1.2")

		assert.True(t, limiter.Allow(ip1))
		assert.True(t, limiter.Allow(ip1))
		assert.False(t, limiter.Allow(ip1), "ip1 should be rate limited")

		assert.True(t, limiter.Allow(ip2), "ip2 should not be affected by ip1 limit")
		assert.True(t, limiter.Allow(ip2))
		assert.False(t, limiter.Allow(ip2), "ip2 should now be rate limited")
	})

	t.Run("handles concurrent requests", func(t *testing.T) {
		limiter := newIPLimiter(100, 100, time.Minute, 0)

		ip := net.ParseIP("10.0.0.1")
		var wg sync.WaitGroup

		for range 50 {
			wg.Go(func() {
				limiter.Allow(ip)
			})
		}

		wg.Wait()
	})

	t.Run("handles invalid IP", func(t *testing.T) {
		limiter := newIPLimiter(10, 10, time.Minute, 0)

		assert.False(t, limiter.Allow(nil))
		assert.False(t, limiter.Allow(net.IP{}))
	})

	t.Run("denies new IPs when cache is at max size", func(t *testing.T) {
		limiter := newIPLimiter(10, 10, time.Minute, 2)

		ip1 := net.ParseIP("192.168.1.1")
		ip2 := net.ParseIP("192.168.1.2")
		ip3 := net.ParseIP("192.168.1.3")

		assert.True(t, limiter.Allow(ip1), "ip1 should be allowed (cache slot 1)")
		assert.True(t, limiter.Allow(ip2), "ip2 should be allowed (cache slot 2)")
		assert.False(t, limiter.Allow(ip3), "ip3 should be denied (cache full)")

		// Known IPs are still served even when the cache is full.
		assert.True(t, limiter.Allow(ip1), "ip1 should still be allowed")
		assert.True(t, limiter.Allow(ip2), "ip2 should still be allowed")
	})

	t.Run("starts cleanup goroutine", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		limiter := newIPLimiter(1, 1, 50*time.Millisecond, 0)
		limiter.Start(ctx)

		ip := net.ParseIP("10.0.0.1")
		assert.True(t, limiter.Allow(ip))
		assert.False(t, limiter.Allow(ip), "should be limited")

		time.Sleep(100 * time.Millisecond)

		assert.True(t, limiter.Allow(ip), "after cleanup, should allow again")

		cancel()
	})
}
