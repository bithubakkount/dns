package app

import (
	"net"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type clientLimiter struct {
	mu     sync.Mutex
	items  map[string]*clientBucket
	rps    rate.Limit
	burst  int
	lastGC time.Time
}

type clientBucket struct {
	lim  *rate.Limiter
	last time.Time
}

func newClientLimiter(c Config) *clientLimiter {
	return &clientLimiter{
		items:  make(map[string]*clientBucket),
		rps:    rate.Limit(c.RateLimit.PerClientRequestsPerSecond),
		burst:  c.RateLimit.PerClientBurst,
		lastGC: time.Now(),
	}
}

func (l *clientLimiter) Allow(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.items[host]
	if b == nil {
		b = &clientBucket{lim: rate.NewLimiter(l.rps, l.burst), last: time.Now()}
		l.items[host] = b
	} else {
		b.last = time.Now()
	}
	if time.Since(l.lastGC) > 5*time.Minute {
		for k, v := range l.items {
			if time.Since(v.last) > 10*time.Minute {
				delete(l.items, k)
			}
		}
		l.lastGC = time.Now()
	}
	return b.lim.Allow()
}
