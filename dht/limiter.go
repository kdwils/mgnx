package dht

import (
	"context"
	"net"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ipLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*clientLimiter
	cfg      perIPLimit
}

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type perIPLimit struct {
	rate     float64
	burst    int
	maxAddrs int
	ttl      time.Duration
}

func newIPLimiter(rateLimit float64, burst int) *ipLimiter {
	return &ipLimiter{
		limiters: make(map[string]*clientLimiter),
		cfg: perIPLimit{
			rate:     rateLimit,
			burst:    burst,
			maxAddrs: 10000,
			ttl:      5 * time.Minute,
		},
	}
}

func (l *ipLimiter) Allow(ip net.IP) bool {
	key := ip.String()
	l.mu.RLock()
	cl, ok := l.limiters[key]
	l.mu.RUnlock()

	if ok {
		return cl.limiter.Allow()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if cl, ok := l.limiters[key]; ok {
		return cl.limiter.Allow()
	}

	if len(l.limiters) >= l.cfg.maxAddrs {
		l.cleanup()
		if len(l.limiters) >= l.cfg.maxAddrs {
			return false
		}
	}

	cl = &clientLimiter{
		limiter:  rate.NewLimiter(rate.Limit(l.cfg.rate), l.cfg.burst),
		lastSeen: time.Now(),
	}
	l.limiters[key] = cl
	return cl.limiter.Allow()
}

func (l *ipLimiter) cleanup() {
	threshold := time.Now().Add(-l.cfg.ttl)
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, v := range l.limiters {
		if v.lastSeen.Before(threshold) {
			delete(l.limiters, k)
		}
	}
}

func (l *ipLimiter) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(l.cfg.ttl)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				l.cleanup()
			case <-ctx.Done():
				return
			}
		}
	}()
}
