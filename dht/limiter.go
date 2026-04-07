package dht

import (
	"context"
	"net"
	"time"

	"github.com/kdwils/mgnx/pkg/cache"
	"golang.org/x/time/rate"
)

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type ipLimiter struct {
	cache *cache.Cache[string, *clientLimiter]
	cfg   perIPLimit
}

type perIPLimit struct {
	rate  float64
	burst int
	ttl   time.Duration
}

func newIPLimiter(rateLimit float64, burst int, ttl time.Duration) *ipLimiter {
	cl := cache.New[string, *clientLimiter](
		cache.WithCleanup[string, *clientLimiter](
			ttl,
			func(_ string, cl *clientLimiter) bool {
				return time.Since(cl.lastSeen) > ttl
			},
		),
	)
	return &ipLimiter{
		cache: cl,
		cfg: perIPLimit{
			rate:  rateLimit,
			burst: burst,
			ttl:   ttl,
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
		return cl.limiter.Allow()
	}

	cl = &clientLimiter{
		limiter:  rate.NewLimiter(rate.Limit(l.cfg.rate), l.cfg.burst),
		lastSeen: time.Now(),
	}
	l.cache.Set(key, cl)
	return cl.limiter.Allow()
}

func (l *ipLimiter) Start(ctx context.Context) {
	l.cache.StartCleanup(ctx)
}
