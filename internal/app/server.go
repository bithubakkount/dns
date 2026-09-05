package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

var Version = "0.3.3"

type Server struct {
	cfg                                Config
	cache                              *Cache
	logger                             *slog.Logger
	udp, tcp                           *dns.Server
	http                               *http.Server
	group                              singleflight.Group
	limiter                            *rate.Limiter
	clientLimiter                      *clientLimiter
	total, hits, misses, upstreamFails atomic.Uint64
	wg                                 sync.WaitGroup
	shutdownOnce                       sync.Once
	shutdownErr                        error
	ready                              atomic.Bool
	dohOnce                            sync.Once
	dohHTTP                            *http.Client
}

func NewServer(cfg Config) (*Server, error) {
	var h slog.Handler
	opts := &slog.HandlerOptions{Level: slogLevel(cfg.Logging.Level)}
	if cfg.Logging.Format == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return &Server{cfg: cfg, cache: NewCache(cfg), logger: slog.New(h), limiter: rate.NewLimiter(rate.Limit(cfg.RateLimit.RequestsPerSecond), cfg.RateLimit.Burst), clientLimiter: newClientLimiter(cfg)}, nil
}

func (s *Server) Start(ctx context.Context) error {
	addr := net.JoinHostPort(s.cfg.Listen.Address, fmt.Sprint(s.cfg.Listen.Port))
	httpAddr := net.JoinHostPort(s.cfg.HTTP.Address, fmt.Sprint(s.cfg.HTTP.Port))

	// Bind every listener before declaring the server ready. This prevents a
	// partially-started daemon from advertising readiness when one endpoint
	// failed to bind (for example, because another process owns :8080).
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve DNS UDP address %s: %w", addr, err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("bind DNS UDP %s: %w", addr, err)
	}

	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("resolve DNS TCP address %s: %w", addr, err)
	}
	tcpConn, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("bind DNS TCP %s: %w", addr, err)
	}

	httpListener, err := net.Listen("tcp", httpAddr)
	if err != nil {
		_ = tcpConn.Close()
		_ = udpConn.Close()
		return fmt.Errorf("bind HTTP %s: %w", httpAddr, err)
	}

	s.udp = &dns.Server{
		PacketConn: udpConn,
		Handler:    dns.HandlerFunc(s.handleDNS),
	}
	s.tcp = &dns.Server{
		Listener: tcpConn,
		Handler:  dns.HandlerFunc(s.handleDNS),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/live", s.live)
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/ready", s.readyz)
	mux.Handle("/metrics", promhttp.Handler())
	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	errCh := make(chan error, 3)
	startCh := make(chan struct{}, 3)
	s.udp.NotifyStartedFunc = func() { startCh <- struct{}{} }
	s.tcp.NotifyStartedFunc = func() { startCh <- struct{}{} }

	s.wg.Add(3)
	go func() {
		defer s.wg.Done()
		errCh <- s.udp.ActivateAndServe()
	}()
	go func() {
		defer s.wg.Done()
		errCh <- s.tcp.ActivateAndServe()
	}()
	go func() {
		defer s.wg.Done()
		startCh <- struct{}{}
		errCh <- s.http.Serve(httpListener)
	}()

	// Wait until every already-bound listener has entered its serving loop.
	// Only then is readiness advertised and the successful start logged.
	for started := 0; started < 3; started++ {
		select {
		case <-startCh:
		case err := <-errCh:
			if err == nil || errors.Is(err, http.ErrServerClosed) {
				started++
				continue
			}
			s.ready.Store(false)
			_ = udpConn.Close()
			_ = tcpConn.Close()
			_ = httpListener.Close()
			s.wg.Wait()
			_ = s.cache.Close()
			return fmt.Errorf("start server: %w", err)
		case <-ctx.Done():
			_ = udpConn.Close()
			_ = tcpConn.Close()
			_ = httpListener.Close()
			s.wg.Wait()
			_ = s.cache.Close()
			return ctx.Err()
		}
	}

	s.ready.Store(true)
	s.logger.Info("localdns started", "dns", addr, "http", httpAddr)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout.Std())
		defer cancel()
		if err := s.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		s.logger.Info("localdns stopped", "reason", ctx.Err())
		return nil
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		s.ready.Store(false)
		s.logger.Error("server stopped unexpectedly", "error", err)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout.Std())
		defer cancel()
		_ = s.Shutdown(shutdownCtx)
		return err
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.ready.Store(false)
		var errs []error
		if s.udp != nil {
			if err := s.udp.ShutdownContext(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if s.tcp != nil {
			if err := s.tcp.ShutdownContext(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if s.http != nil {
			if err := s.http.Shutdown(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		s.wg.Wait()
		if err := s.cache.Close(); err != nil {
			errs = append(errs, err)
		}
		s.shutdownErr = errors.Join(errs...)
	})
	return s.shutdownErr
}

func (s *Server) handleDNS(w dns.ResponseWriter, r *dns.Msg) {
	s.total.Add(1)
	queries.Inc()
	if len(r.Question) == 0 {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeFormatError)
		_ = w.WriteMsg(m)
		return
	}
	host, _, _ := net.SplitHostPort(w.RemoteAddr().String())
	if !allowedIP(net.ParseIP(host), s.cfg.ACL.Allow) {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeRefused)
		_ = w.WriteMsg(m)
		return
	}
	if !s.limiter.Allow() || !s.clientLimiter.Allow(w.RemoteAddr()) {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeRefused)
		_ = w.WriteMsg(m)
		return
	}

	q := r.Question[0]
	if local, ok := s.localRecord(r); ok {
		_ = w.WriteMsg(local)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Upstreams.Timeout.Std()*time.Duration(s.cfg.Upstreams.Attempts))
	defer cancel()
	if msg, ok, stale := s.cache.Get(ctx, &q); ok {
		s.hits.Add(1)
		cacheHits.WithLabelValues("cache", fmt.Sprint(stale)).Inc()
		msg.Id = r.Id
		msg.RecursionDesired = r.RecursionDesired
		msg.RecursionAvailable = true
		if stale {
			go s.refresh(q)
		}
		_ = w.WriteMsg(msg)
		return
	}
	s.misses.Add(1)
	cacheMisses.Inc()

	key := fmt.Sprintf("%s/%d/%d", normalizeName(q.Name), q.Qtype, q.Qclass)
	v, err, _ := s.group.Do(key, func() (interface{}, error) { return s.resolve(ctx, &q) })
	if err != nil {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
		return
	}
	msg := v.(*dns.Msg).Copy()
	msg.Id = r.Id
	_ = w.WriteMsg(msg)
}

func (s *Server) refresh(q dns.Question) {
	timeout := s.cfg.Upstreams.Timeout.Std()
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	key := fmt.Sprintf("%s/%d/%d", normalizeName(q.Name), q.Qtype, q.Qclass)
	_, _, _ = s.group.Do(key, func() (interface{}, error) {
		return s.resolve(ctx, &q)
	})
}

func (s *Server) resolve(ctx context.Context, q *dns.Question) (*dns.Msg, error) {
	req := new(dns.Msg)
	req.SetQuestion(q.Name, q.Qtype)
	req.RecursionDesired = true

	resp, err := s.exchange(ctx, req)
	if err != nil {
		s.upstreamFails.Add(1)
		upstreamFailures.Inc()
		return nil, err
	}
	if resp == nil {
		s.upstreamFails.Add(1)
		upstreamFailures.Inc()
		return nil, errors.New("upstream returned empty response")
	}
	if resp.Rcode != dns.RcodeSuccess && resp.Rcode != dns.RcodeNameError {
		s.upstreamFails.Add(1)
		upstreamFailures.Inc()
		return nil, fmt.Errorf("upstream returned DNS rcode %d", resp.Rcode)
	}

	s.cache.Set(ctx, q, resp)
	return resp, nil
}

func (s *Server) exchange(ctx context.Context, msg *dns.Msg) (*dns.Msg, error) {
	if len(msg.Question) != 1 {
		return nil, fmt.Errorf("exactly one DNS question is required")
	}

	cfg := s.cfg
	timeout := cfg.Upstreams.Timeout.Std()
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for attempt := 0; attempt < max(1, cfg.Upstreams.Attempts); attempt++ {
		for _, upstream := range cfg.Upstreams.Servers {
			resp, err := s.exchangeUpstream(ctx, upstream, msg)
			if err != nil {
				continue
			}
			if resp != nil && resp.Truncated && strings.EqualFold(cfg.Upstreams.Transport, "udp") {
				if tcpResp, tcpErr := s.exchangeDNSOverTCP(ctx, upstream, msg); tcpErr == nil {
					resp = tcpResp
				}
			}
			if resp != nil {
				return resp, nil
			}
		}
	}

	return nil, fmt.Errorf("all upstreams failed")
}

func (s *Server) exchangeUpstream(ctx context.Context, upstream string, msg *dns.Msg) (*dns.Msg, error) {
	cfg := s.cfg
	transport := strings.ToLower(strings.TrimSpace(cfg.Upstreams.Transport))

	switch {
	case strings.HasPrefix(upstream, "https://"):
		transport = "doh"
	case strings.HasPrefix(upstream, "tls://"):
		transport = "dot"
	case strings.HasPrefix(upstream, "tcp://"):
		transport = "tcp"
	case strings.HasPrefix(upstream, "udp://"):
		transport = "udp"
	}

	switch transport {
	case "doh":
		return s.exchangeDoH(ctx, upstream, msg)
	case "dot":
		return s.exchangeDoT(ctx, upstream, msg)
	case "tcp":
		return s.exchangeDNSOverTCP(ctx, strings.TrimPrefix(upstream, "tcp://"), msg)
	default:
		return s.exchangeDNSOverUDP(ctx, strings.TrimPrefix(upstream, "udp://"), msg)
	}
}

func (s *Server) exchangeDNSOverUDP(ctx context.Context, upstream string, msg *dns.Msg) (*dns.Msg, error) {
	client := &dns.Client{Net: "udp"}
	in, _, err := client.ExchangeContext(ctx, msg, upstream)
	if err != nil {
		return nil, err
	}
	return in, nil
}

func (s *Server) exchangeDNSOverTCP(ctx context.Context, upstream string, msg *dns.Msg) (*dns.Msg, error) {
	in, err := dns.ExchangeContext(ctx, msg, upstream)
	return in, err
}

func (s *Server) exchangeDoT(ctx context.Context, upstream string, msg *dns.Msg) (*dns.Msg, error) {
	cfg := s.cfg
	addr := strings.TrimPrefix(upstream, "tls://")
	serverName := cfg.Upstreams.DoTServerName

	if host, _, err := net.SplitHostPort(addr); err == nil && serverName == "" {
		serverName = host
	}
	if serverName == "" {
		serverName = addr
	}

	client := &dns.Client{
		Net: "tcp-tls",
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
		},
	}
	in, _, err := client.ExchangeContext(ctx, msg, addr)
	return in, err
}

func (s *Server) exchangeDoH(ctx context.Context, endpoint string, msg *dns.Msg) (*dns.Msg, error) {
	endpoint = strings.TrimSpace(endpoint)
	if !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("invalid DoH endpoint %q", endpoint)
	}

	wire, err := msg.Pack()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	req.Header.Set("User-Agent", "localdns/1.0")

	client := s.dohClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH upstream returned HTTP %d", resp.StatusCode)
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" &&
		!strings.Contains(ct, "application/dns-message") {
		return nil, fmt.Errorf("DoH upstream returned content-type %q", ct)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, err
	}

	out := new(dns.Msg)
	if err := out.Unpack(body); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) localRecord(r *dns.Msg) (*dns.Msg, bool) {
	q := r.Question[0]
	name := normalizeName(q.Name)
	recs, ok := s.cfg.Records[name]
	if !ok {
		return nil, false
	}
	out := new(dns.Msg)
	out.SetReply(r)
	out.Authoritative = true
	out.RecursionAvailable = true
	for _, v := range recs[dns.TypeToString[q.Qtype]] {
		h := dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: 60}
		switch q.Qtype {
		case dns.TypeA:
			if ip := net.ParseIP(v).To4(); ip != nil {
				out.Answer = append(out.Answer, &dns.A{Hdr: h, A: ip})
			}
		case dns.TypeAAAA:
			if ip := net.ParseIP(v).To16(); ip != nil {
				out.Answer = append(out.Answer, &dns.AAAA{Hdr: h, AAAA: ip})
			}
		case dns.TypeCNAME:
			out.Answer = append(out.Answer, &dns.CNAME{Hdr: h, Target: v})
		case dns.TypeTXT:
			out.Answer = append(out.Answer, &dns.TXT{Hdr: h, Txt: []string{v}})
		}
	}
	return out, true
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"alive","version":"` + Version + `"}` + "\n"))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready","version":"` + Version + `"}` + "\n"))
		return
	}
	s.health(w, r)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	redisOK := s.cache.Ping(ctx) == nil
	status := map[string]interface{}{"status": "ok", "redis": "ok", "ready": s.ready.Load(), "version": Version}
	if !redisOK {
		status["status"] = "degraded"
		status["redis"] = "degraded"
		// DNS continues to operate with Redis degraded; health is diagnostic.
	}
	w.Header().Set("Content-Type", "application/json")
	if !s.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else if !redisOK {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(status)
}

func slogLevel(v string) slog.Level {
	switch strings.ToLower(v) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
func (s *Server) dohClient() *http.Client {
	s.dohOnce.Do(func() {
		cfg := s.cfg
		transport := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		if cfg.Upstreams.SOCKS5Proxy != "" {
			proxyURL, err := url.Parse("socks5h://" + cfg.Upstreams.SOCKS5Proxy)
			if err == nil {
				transport.Proxy = http.ProxyURL(proxyURL)
			}
		}
		s.dohHTTP = &http.Client{
			Transport: transport,
			Timeout:   maxDuration(cfg.Upstreams.Timeout.Std(), 2*time.Second),
		}
	})
	return s.dohHTTP
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
