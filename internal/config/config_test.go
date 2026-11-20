// 本文件验证配置未知字段拒绝和时间字段解析行为。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadRejectsUnknownFields 验证未知配置字段会被严格拒绝。
func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':8080'\n  typo: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want unknown field error")
	}
}

// TestLoadParsesListenAddress 验证服务监听地址来自 YAML 配置。
func TestLoadParsesListenAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':9090'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Server.Listen != ":9090" {
		t.Fatalf("Listen = %q, want :9090", config.Server.Listen)
	}
}

// TestLoadDisablesDebugLoggingByDefault 的测试动机是保证生产配置未显式开启时不会写入请求内容。
func TestLoadDisablesDebugLoggingByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':9090'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Debug.Enabled {
		t.Fatal("Debug.Enabled = true, want disabled by default")
	}
}

// TestLoadParsesAuthAPIKey 验证可选的 API Key 可从配置中读取。
func TestLoadParsesAuthAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':9090'\nauth:\n  api_key: 'my-secret-key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Auth.APIKey != "my-secret-key" {
		t.Fatalf("Auth.APIKey = %q, want my-secret-key", config.Auth.APIKey)
	}
}

// TestLoadEnablesDebugLoggingExplicitly 的测试动机是保留排查协议问题时主动开启日志的能力。
func TestLoadEnablesDebugLoggingExplicitly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':9090'\ndebug:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Debug.Enabled {
		t.Fatal("Debug.Enabled = false, want explicitly enabled")
	}
}

// TestLoadParsesProxyPoolsAndRoutes 验证动态命名代理池与分流规则的解析。
func TestLoadParsesProxyPoolsAndRoutes(t *testing.T) {
	yaml := `server:
  listen: ':9090'
devin:
  proxy_pools:
    us:
      - "socks5://u:p@us1.example.com:1080"
      - "http://127.0.0.1:8081"
    hk: "http://127.0.0.1:7890"
  routes:
    - models: ["swe-*", "SWE-*"]
      pool: us
    - models: "glm-*"
      pool: direct
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(config.Devin.ProxyPools["us"]); got != 2 {
		t.Fatalf("pool us addresses = %d, want 2", got)
	}
	if got := config.Devin.ProxyPools["hk"]; len(got) != 1 || got[0] != "http://127.0.0.1:7890" {
		t.Fatalf("pool hk = %v, want single scalar parsed as list", got)
	}
	if got := len(config.Devin.Routes); got != 2 {
		t.Fatalf("routes = %d, want 2", got)
	}
	if got := config.Devin.Routes[1].Models; len(got) != 1 || got[0] != "glm-*" {
		t.Fatalf("route[1].models = %v, want scalar shorthand parsed", got)
	}
}

// TestLoadRejectsInvalidProxyRouting 验证非法代理池/分流配置在启动即被拒绝。
func TestLoadRejectsInvalidProxyRouting(t *testing.T) {
	base := "server:\n  listen: ':9090'\ndevin:\n"
	cases := []struct {
		name string
		yaml string
	}{
		{"reserved pool name", base + "  proxy_pools:\n    direct:\n      - 'http://127.0.0.1:1'\n"},
		{"empty pool", base + "  proxy_pools:\n    us: []\n"},
		{"bad scheme", base + "  proxy_pools:\n    us:\n      - 'ftp://127.0.0.1:1'\n"},
		{"route unknown pool", base + "  routes:\n    - models: ['*']\n      pool: ghost\n"},
		{"route missing pool", base + "  routes:\n    - models: ['*']\n"},
		{"route empty models", base + "  routes:\n    - pool: direct\n"},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("%s: Load() error = nil, want validation error", tc.name)
		}
	}
}
