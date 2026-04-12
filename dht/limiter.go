package dht

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/kdwils/mgnx/pkg/cache"
	"golang.org/x/time/rate"
)

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen atomic.Int64 // unix nanoseconds
}

type ipLimiter struct {
	cache *cache.Cache[string, *clientLimiter]
	cfg   perIPLimit
}

type perIPLimit struct {
	rate    float64
	burst   int
	ttl     time.Duration
	maxSize int // 0 = unlimited
}

func newIPLimiter(rateLimit float64, burst int, ttl time.Duration, maxSize int) *ipLimiter {
	cl := cache.New[string, *clientLimiter](
		cache.WithCleanup[string, *clientLimiter](
			ttl,
			func(_ string, cl *clientLimiter) bool {
				return time.Since(time.Unix(0, cl.lastSeen.Load())) > ttl
			},
		),
	)
	return &ipLimiter{
		cache: cl,
		cfg: perIPLimit{
			rate:    rateLimit,
			burst:   burst,
			ttl:     ttl,
			maxSize: maxSize,
		},
	}
}

func (l *ipLimiter) Allow(ip net.IP) bool {
	if ip == nil || len(ip) == 0 {
		return false
	}
	key := ip.String()
	cl, ok := l.cache.Get(key)
	if ok {
		cl.lastSeen.Store(time.Now().UnixNano())
		return cl.limiter.Allow()
	}

	if l.cfg.maxSize > 0 && l.cache.Size() >= l.cfg.maxSize {
		return false
	}

	cl = &clientLimiter{
		limiter: rate.NewLimiter(rate.Limit(l.cfg.rate), l.cfg.burst),
	}
	cl.lastSeen.Store(time.Now().UnixNano())
	l.cache.Set(key, cl)
	return cl.limiter.Allow()
}

func (l *ipLimiter) Start(ctx context.Context) {
	l.cache.StartCleanup(ctx)
}
