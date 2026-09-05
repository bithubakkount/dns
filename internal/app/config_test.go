package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "localdns.yaml")
	if err := os.WriteFile(p, []byte(`
listen:
  address: 127.0.0.1
  port: 5353
redis:
  timeout: 500ms
cache:
  max_ttl: 1h
  min_ttl: 1s
  stale_ttl: 30s
upstreams:
  servers: ["1.1.1.1:53"]
  timeout: 2s
  attempts: 2
`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen.Port != 5353 {
		t.Fatalf("port=%d", c.Listen.Port)
	}
	if c.Cache.MaxTTL.Std().Hours() != 1 {
		t.Fatalf("max ttl=%v", c.Cache.MaxTTL.Std())
	}
}
