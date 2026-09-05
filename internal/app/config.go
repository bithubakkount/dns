package app

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	v, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	*d = Duration(v)
	return nil
}
func (d Duration) Std() time.Duration { return time.Duration(d) }

type Config struct {
	Listen struct {
		Address string `yaml:"address"`
		Port    int    `yaml:"port"`
	} `yaml:"listen"`
	HTTP struct {
		Address string `yaml:"address"`
		Port    int    `yaml:"port"`
	} `yaml:"http"`
	Redis struct {
		Address  string   `yaml:"address"`
		Password string   `yaml:"password"`
		DB       int      `yaml:"db"`
		Timeout  Duration `yaml:"timeout"`
	} `yaml:"redis"`
	Cache struct {
		MaxEntries int      `yaml:"max_entries"`
		MaxTTL     Duration `yaml:"max_ttl"`
		MinTTL     Duration `yaml:"min_ttl"`
		StaleTTL   Duration `yaml:"stale_ttl"`
	} `yaml:"cache"`
	Upstreams struct {
		Servers       []string `yaml:"servers"`
		Timeout       Duration `yaml:"timeout"`
		Attempts      int      `yaml:"attempts"`
		Transport     string   `yaml:"transport"`
		DoHServerName string   `yaml:"doh_server_name"`
		DoTServerName string   `yaml:"dot_server_name"`
		SOCKS5Proxy   string   `yaml:"socks5_proxy"`
	} `yaml:"upstreams"`
	ACL struct {
		Allow []string `yaml:"allow"`
	} `yaml:"acl"`
	RateLimit struct {
		RequestsPerSecond          int `yaml:"requests_per_second"`
		Burst                      int `yaml:"burst"`
		PerClientRequestsPerSecond int `yaml:"per_client_requests_per_second"`
		PerClientBurst             int `yaml:"per_client_burst"`
	} `yaml:"rate_limit"`
	ShutdownTimeout Duration `yaml:"shutdown_timeout"`
	Logging         struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"logging"`
	Records map[string]map[string][]string `yaml:"records"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, err
	}

	if c.Listen.Address == "" {
		c.Listen.Address = "127.0.0.1"
	}
	if c.Listen.Port == 0 {
		c.Listen.Port = 53
	}
	if c.HTTP.Address == "" {
		c.HTTP.Address = "127.0.0.1"
	}
	if c.HTTP.Port == 0 {
		c.HTTP.Port = 8080
	}
	if c.Redis.Address == "" {
		c.Redis.Address = "127.0.0.1:6379"
	}
	if c.Redis.Timeout == 0 {
		c.Redis.Timeout = Duration(500 * time.Millisecond)
	}
	if c.Cache.MaxEntries == 0 {
		c.Cache.MaxEntries = 20000
	}
	if c.Cache.MaxTTL == 0 {
		c.Cache.MaxTTL = Duration(24 * time.Hour)
	}
	if c.Cache.MinTTL == 0 {
		c.Cache.MinTTL = Duration(time.Second)
	}
	if c.Cache.StaleTTL == 0 {
		c.Cache.StaleTTL = Duration(30 * time.Second)
	}
	if c.Upstreams.Timeout == 0 {
		c.Upstreams.Timeout = Duration(2 * time.Second)
	}
	if c.Upstreams.Attempts < 1 {
		c.Upstreams.Attempts = 2
	}
	if c.Upstreams.Transport == "" {
		c.Upstreams.Transport = "udp"
	}
	if c.Upstreams.Transport != "udp" && c.Upstreams.Transport != "tcp" && c.Upstreams.Transport != "dot" && c.Upstreams.Transport != "doh" {
		return c, fmt.Errorf("unsupported upstream transport %q", c.Upstreams.Transport)
	}
	if len(c.Upstreams.Servers) == 0 {
		c.Upstreams.Servers = []string{"1.1.1.1:53", "8.8.8.8:53"}
	}
	if c.RateLimit.RequestsPerSecond == 0 {
		c.RateLimit.RequestsPerSecond = 200
	}
	if c.RateLimit.Burst == 0 {
		c.RateLimit.Burst = 400
	}
	if c.RateLimit.PerClientRequestsPerSecond == 0 {
		c.RateLimit.PerClientRequestsPerSecond = 50
	}
	if c.RateLimit.PerClientBurst == 0 {
		c.RateLimit.PerClientBurst = 100
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = Duration(10 * time.Second)
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}

	if net.ParseIP(c.Listen.Address) == nil && c.Listen.Address != "localhost" {
		return c, errors.New("listen.address must be an IP address")
	}
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		return c, errors.New("invalid DNS port")
	}
	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		return c, errors.New("invalid HTTP port")
	}
	if len(c.Upstreams.Servers) == 0 {
		return c, errors.New("at least one upstream is required")
	}

	for _, cidr := range c.ACL.Allow {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return c, fmt.Errorf("invalid ACL %q: %w", cidr, err)
		}
	}
	return c, nil
}

func allowedIP(ip net.IP, cidrs []string) bool {
	if ip == nil {
		return false
	}
	if len(cidrs) == 0 {
		return true
	}
	for _, s := range cidrs {
		_, n, err := net.ParseCIDR(s)
		if err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSuffix(s, "."))
}
