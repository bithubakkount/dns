package app

import (
	"container/list"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/redis/go-redis/v9"
)

type cacheEntry struct {
	Msg       []byte    `json:"msg"`
	StoredAt  time.Time `json:"stored_at"`
	ExpiresAt time.Time `json:"expires_at"`
	StaleAt   time.Time `json:"stale_at"`
}

type memItem struct {
	key string
	val cacheEntry
}

type Cache struct {
	mu                       sync.Mutex
	mem                      map[string]*list.Element
	lru                      *list.List
	max                      int
	maxTTL, minTTL, staleTTL time.Duration
	redis                    *redis.Client
}

func NewCache(c Config) *Cache {
	r := redis.NewClient(&redis.Options{
		Addr:         c.Redis.Address,
		Password:     c.Redis.Password,
		DB:           c.Redis.DB,
		Protocol:     2,
		DialTimeout:  c.Redis.Timeout.Std(),
		ReadTimeout:  c.Redis.Timeout.Std(),
		WriteTimeout: c.Redis.Timeout.Std(),
		PoolSize:     16,
		MinIdleConns: 2,
	})
	maxEntries := c.Cache.MaxEntries
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &Cache{
		mem: make(map[string]*list.Element, maxEntries), lru: list.New(), max: maxEntries,
		maxTTL: c.Cache.MaxTTL.Std(), minTTL: c.Cache.MinTTL.Std(), staleTTL: c.Cache.StaleTTL.Std(), redis: r,
	}
}

func (c *Cache) Close() error                   { return c.redis.Close() }
func (c *Cache) Ping(ctx context.Context) error { return c.redis.Ping(ctx).Err() }

func (c *Cache) key(q *dns.Question) string {
	return "localdns:v1:" + normalizeName(q.Name) + ":" + dns.TypeToString[q.Qtype] + ":" + dns.ClassToString[q.Qclass]
}

func rewriteTTL(msg *dns.Msg, age time.Duration) {
	if age <= 0 {
		return
	}
	secs := uint32(age / time.Second)
	if secs == 0 {
		return
	}
	rewrite := func(rrs []dns.RR) {
		for _, rr := range rrs {
			if rr == nil {
				continue
			}
			h := rr.Header()
			if h.Ttl > secs {
				h.Ttl -= secs
			} else {
				h.Ttl = 0
			}
		}
	}
	rewrite(msg.Answer)
	rewrite(msg.Ns)
	rewrite(msg.Extra)
}

func (c *Cache) Get(ctx context.Context, q *dns.Question) (*dns.Msg, bool, bool) {
	k, now := c.key(q), time.Now()

	c.mu.Lock()
	if el, ok := c.mem[k]; ok {
		e := el.Value.(memItem).val
		c.lru.MoveToFront(el)
		c.mu.Unlock()
		if now.Before(e.StaleAt) {
			m := new(dns.Msg)
			if m.Unpack(e.Msg) == nil {
				rewriteTTL(m, now.Sub(e.StoredAt))
				return m, true, !now.Before(e.ExpiresAt)
			}
		}
	} else {
		c.mu.Unlock()
	}

	b, err := c.redis.Get(ctx, k).Bytes()
	if err != nil {
		return nil, false, false
	}
	var e cacheEntry
	if json.Unmarshal(b, &e) != nil || !now.Before(e.StaleAt) {
		return nil, false, false
	}

	m := new(dns.Msg)
	if m.Unpack(e.Msg) != nil {
		return nil, false, false
	}
	rewriteTTL(m, now.Sub(e.StoredAt))

	c.putMemory(k, e)
	return m, true, !now.Before(e.ExpiresAt)
}

func (c *Cache) Set(ctx context.Context, q *dns.Question, msg *dns.Msg) {
	ttl := minTTL(msg)
	if ttl < c.minTTL {
		ttl = c.minTTL
	}
	if ttl > c.maxTTL {
		ttl = c.maxTTL
	}

	raw, err := msg.Pack()
	if err != nil {
		return
	}
	now := time.Now()
	e := cacheEntry{Msg: raw, StoredAt: now, ExpiresAt: now.Add(ttl), StaleAt: now.Add(ttl + c.staleTTL)}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}

	k := c.key(q)
	cacheTTL := ttl + c.staleTTL
	if cacheTTL > 0 {
		_ = c.redis.Set(ctx, k, b, cacheTTL).Err()
	}
	c.putMemory(k, e)
}

func (c *Cache) putMemory(k string, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.mem[k]; ok {
		el.Value = memItem{key: k, val: e}
		c.lru.MoveToFront(el)
		return
	}
	el := c.lru.PushFront(memItem{key: k, val: e})
	c.mem[k] = el
	for len(c.mem) > c.max {
		old := c.lru.Back()
		if old == nil {
			break
		}
		delete(c.mem, old.Value.(memItem).key)
		c.lru.Remove(old)
	}
}

func (c *Cache) Flush(ctx context.Context) error {
	c.mu.Lock()
	c.mem = make(map[string]*list.Element, c.max)
	c.lru.Init()
	c.mu.Unlock()
	var keys []string
	iter := c.redis.Scan(ctx, 0, "localdns:v1:*", 0).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return err
	}
	if len(keys) > 0 {
		return c.redis.Del(ctx, keys...).Err()
	}
	return nil
}

func minTTL(m *dns.Msg) time.Duration {
	var min uint32
	found := false
	check := func(rrs []dns.RR) {
		for _, rr := range rrs {
			if rr == nil {
				continue
			}
			h := rr.Header()
			if h.Rrtype == dns.TypeOPT {
				continue
			}
			if !found || h.Ttl < min {
				min = h.Ttl
				found = true
			}
		}
	}
	check(m.Answer)
	check(m.Ns)
	check(m.Extra)
	if !found {
		return 30 * time.Second
	}
	return time.Duration(min) * time.Second
}
