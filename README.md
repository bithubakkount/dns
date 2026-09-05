# localdns

Production-oriented local DNS caching resolver for macOS/Apple Silicon.

## Architecture

- Go DNS server: UDP + TCP
- L1 bounded LRU memory cache
- L2 Redis cache
- TTL-correct cache responses
- stale-while-revalidate
- singleflight request coalescing
- local A/AAAA/CNAME/TXT records
- ACLs
- global + per-client rate limiting
- UDP/TCP/DoT/DoH upstream transports
- Prometheus metrics
- health/readiness endpoint
- structured logging
- Homebrew service definition

## Important security default

The resolver binds to `127.0.0.1` and only permits loopback clients. Do not change this to `0.0.0.0` without configuring an appropriate ACL and macOS firewall policy.

## Install

```bash
brew install go redis
brew services start redis
cd ~/Projects/localdns
go mod tidy
make build
```

Run manually:

```bash
sudo ./bin/localdns-darwin-arm64 --config configs/localdns.yaml
```

Test:

```bash
dig @127.0.0.1 example.com
curl http://127.0.0.1:8080/live
curl http://127.0.0.1:8080/ready
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/metrics
```

## Homebrew

The Formula/localdns.rb file is a publishable template. Replace the GitHub repository owner and release SHA256 before using it as a public tap.

Homebrew supports formula-defined service blocks and `require_root`; `brew services` uses macOS launchd. See Homebrew's Formula Cookbook.

## Production hardening roadmap

The resolver core is deliberately conservative. Before exposing it beyond localhost, add and test:

- DoH/DoT upstream transports
- DNSSEC validation
- per-client rate limiting
- upstream health scoring/circuit breaking
- comprehensive protocol fuzzing
- configuration reload
- encrypted Redis when Redis is remote
- signed releases and SBOM
- CI release automation


## Personal-device profile

This project is intended for personal devices by default. Keep the DNS listener on `127.0.0.1` unless you deliberately want LAN clients. Per-client limiting is still enabled as defense-in-depth.

### DoH example

```yaml
upstreams:
  transport: doh
  servers:
    - https://dns.google/dns-query
    - https://cloudflare-dns.com/dns-query
  timeout: 2s
  attempts: 2
```

### DoT example

```yaml
upstreams:
  transport: dot
  servers:
    - 1.1.1.1:853
    - 8.8.8.8:853
  dot_server_name: cloudflare-dns.com
```

## DNSSEC

The resolver preserves DNSSEC records returned by upstreams and requests recursive resolution. Full local DNSSEC chain validation is intentionally not enabled in this personal-device release; if DNSSEC validation is a hard requirement, use a validating recursive resolver upstream or add a dedicated validation library before claiming end-to-end local validation.


## Optimized Tor/DoH profile

This personal-device profile sends external DNS queries to the configured DoH endpoints through the Tor SOCKS5 listener at `127.0.0.1:9050`. It does not use Unbound.

Before starting localdns on port 53, verify Tor is listening on `127.0.0.1:9050` and keep localdns bound to loopback unless you intentionally want LAN clients.

The DoH HTTP transport is connection-reused, bounded by explicit timeouts, validates HTTP status/content type, limits response size, and uses a SOCKS5 proxy when configured.

## Lifecycle and graceful shutdown

The daemon handles SIGINT and SIGTERM through its main context. On shutdown it:

1. marks `/ready` as unavailable so new health-based traffic can drain;
2. gracefully shuts down UDP, TCP, and HTTP listeners;
3. waits for active handlers up to `shutdown_timeout`;
4. closes the Redis client;
5. logs a final stopped event.

`/live` is a liveness endpoint, `/ready` reports whether the server is accepting work, and `/health` reports Redis health without treating Redis degradation as a DNS outage.

The default shutdown timeout is 10 seconds and can be changed with `shutdown_timeout` in the YAML configuration.


## 0.3.2

- Corrected upstream failure Prometheus accounting.
- Optimized cache TTL rewriting and cache-key reuse.
- Added singleflight protection to stale-cache background refreshes.
- Hardened bounded L1 cache initialization.

## 0.3.2

Startup now binds UDP DNS, TCP DNS, and the HTTP health/metrics listener before advertising readiness. If any listener fails to bind, previously opened listeners are closed and the process exits without reporting a successful start. DNS listeners use miekg/dns socket activation via `ActivateAndServe`, preserving graceful shutdown behavior.
