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
		limiter := newIPLimiter(10, 10, time.Minute)

		ip := net.ParseIP("192.168.1.1")

		for i := 0; i < 10; i++ {
			assert.True(t, limiter.Allow(ip), "expected allow for request %d", i+1)
		}
	})

	t.Run("denies after burst exceeded", func(t *testing.T) {
		limiter := newIPLimiter(10, 10, time.Minute)

		ip := net.ParseIP("192.168.1.1")

		for i := 0; i < 10; i++ {
			limiter.Allow(ip)
		}

		assert.False(t, limiter.Allow(ip), "expected deny after burst exceeded")
	})

	t.Run("each IP has independent limit", func(t *testing.T) {
		limiter := newIPLimiter(2, 2, time.Minute)

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
		limiter := newIPLimiter(100, 100, time.Minute)

		ip := net.ParseIP("10.0.0.1")
		var wg sync.WaitGroup

		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				limiter.Allow(ip)
			}()
		}

		wg.Wait()
	})

	t.Run("handles invalid IP", func(t *testing.T) {
		limiter := newIPLimiter(10, 10, time.Minute)

		assert.False(t, limiter.Allow(nil))
		assert.False(t, limiter.Allow(net.IP{}))
	})

	t.Run("starts cleanup goroutine", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		limiter := newIPLimiter(1, 1, 50*time.Millisecond)
		limiter.Start(ctx)

		ip := net.ParseIP("10.0.0.1")
		assert.True(t, limiter.Allow(ip))
		assert.False(t, limiter.Allow(ip), "should be limited")

		time.Sleep(100 * time.Millisecond)

		assert.True(t, limiter.Allow(ip), "after cleanup, should allow again")

		cancel()
	})
}
