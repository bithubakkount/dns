package app

import "github.com/prometheus/client_golang/prometheus"

var (
	queries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "localdns_queries_total",
		Help: "Total DNS queries.",
	})

	cacheHits = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "localdns_cache_hits_total",
			Help: "DNS cache hits.",
		},
		[]string{"source", "stale"},
	)

	cacheMisses = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "localdns_cache_misses_total",
		Help: "DNS cache misses.",
	})

	upstreamFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "localdns_upstream_failures_total",
		Help: "Failed upstream resolution attempts.",
	})
)

func init() {
	prometheus.MustRegister(queries, cacheHits, cacheMisses, upstreamFailures)
}
