// 本文件定义服务启动配置及其 YAML 加载和校验逻辑。
//
// Package config 负责加载和校验服务启动配置。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 保存服务启动所需的全部配置；服务运行期间不会热更新。
type Config struct {
	// Server 保存 HTTP 服务配置。
	Server ServerConfig `yaml:"server"`
	// Devin 保存 Devin Connect 上游配置。
	Devin DevinConfig `yaml:"devin"`
	// Debug 保存仅用于本地诊断的日志配置。
	Debug DebugConfig `yaml:"debug"`
	// Dashboard 保存管理面板配置。
	Dashboard DashboardConfig `yaml:"dashboard"`
	// Auth 保存对外 OpenAI 兼容接口的访问控制配置。
	Auth AuthConfig `yaml:"auth"`
}

// ServerConfig 保存 HTTP 服务监听配置。
type ServerConfig struct {
	// Listen 是 HTTP 服务监听地址。
	Listen string `yaml:"listen"`
	// MaxConcurrency 是同时处理的 /v1/* 请求数上限；0 表示使用默认值。
	MaxConcurrency int `yaml:"max_concurrency"`
}

// DevinConfig 保存 Devin Connect 上游调用配置。
type DevinConfig struct {
	// BaseURL 是 Devin Connect 服务的基础地址。
	BaseURL string `yaml:"base_url"`
	// Token 是 Devin session token；不会写入日志。
	Token string `yaml:"token"`
	// Model 是 Devin chat model UID。
	Model string `yaml:"model"`
	// Proxy 是可选的 HTTP/HTTPS/SOCKS5 代理地址；为空时直连或走系统环境变量。
	// 它也是模型分流未命中时的兜底出口。
	Proxy string `yaml:"proxy"`
	// ProxyPools 是动态命名的代理池集合：每个池可放多个代理地址，
	// 池内按请求轮询分摊，避免高并发打满单个代理。
	// 保留池名：direct（直连/系统环境变量代理）、default（= devin.proxy 兜底）。
	ProxyPools map[string]StringList `yaml:"proxy_pools"`
	// Routes 是模型分流规则：从上到下按 uid/标签/系列/分组的通配符匹配
	// （* 任意串、? 单字符，忽略大小写），命中即走对应代理池；
	// 未命中的模型走 devin.proxy（留空则直连）。
	Routes []RouteRule `yaml:"routes"`
	// ForceHTTP1 为 true 时强制使用 HTTP/1.1，每请求独立 TCP 连接，
	// 避免 HTTP/2 单连接多 stream 复用导致的上游并发瓶颈（首字延迟飙升/卡住）。
	// 行为对齐 Devin 客户端多窗口各自独立连接的模式。默认 true。
	ForceHTTP1 *bool `yaml:"force_http1"`
	// MaxNewlines 是上游单次输出允许的最大换行数（超过即以 length 截断）；
	// 0/留空取默认值。长代码生成场景可调大。
	MaxNewlines int `yaml:"max_newlines"`
}

// DebugConfig 保存请求级调试日志配置。
type DebugConfig struct {
	// Enabled 表示是否在配置文件同目录的 logs 下写入请求调试日志。
	Enabled bool `yaml:"enabled"`
}

// DashboardConfig 保存管理面板配置。
type DashboardConfig struct {
	// Enabled 控制是否启用 /panel 管理面板；默认 false（关闭），需显式开启。
	Enabled bool `yaml:"enabled"`
	// Password 是面板访问密码：
	//   留空          → 每次启动自动生成随机密码并打印到启动日志；
	//   "none"        → 免密开放（仅限可信本机/内网环境）；
	//   其他任意字符串 → 固定密码。
	Password string `yaml:"password"`
}

// StringList 同时接受 YAML 标量与字符串列表，统一解析为 []string。
type StringList []string

// UnmarshalYAML 允许 "val" 或 ["a","b"] 两种写法。
func (list *StringList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var single string
		if err := value.Decode(&single); err != nil {
			return err
		}
		*list = StringList{single}
		return nil
	}
	var many []string
	if err := value.Decode(&many); err != nil {
		return err
	}
	*list = many
	return nil
}

// RouteRule 把一组模型通配符模式映射到一个代理池。
type RouteRule struct {
	// Models 是 uid/标签/系列/分组 的通配符模式列表。
	Models StringList `yaml:"models"`
	// Pool 是目标代理池名：proxy_pools 中定义的名字，或内置的 direct / default。
	Pool string `yaml:"pool"`
}

// AuthConfig 保存对外 OpenAI 兼容接口的访问控制配置。
type AuthConfig struct {
	// APIKey 是客户端访问 /v1/* 接口所需的密钥；为空时不启用鉴权。
	APIKey string `yaml:"api_key"`
}

// Load 从 YAML 文件读取并校验配置。
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	var config Config
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return config, nil
}

// Validate 检查配置中的必填项，并设置默认值。
func (config *Config) Validate() error {
	if config.Server.Listen == "" {
		return errors.New("server.listen is required")
	}
	if config.Server.MaxConcurrency <= 0 {
		config.Server.MaxConcurrency = 1024
	}
	// ForceHTTP1 默认开启：HTTP/2 单连接多 stream 复用是并发首字延迟飙升的根因。
	if config.Devin.ForceHTTP1 == nil {
		force := true
		config.Devin.ForceHTTP1 = &force
	}
	return config.validateProxyRouting()
}

// reservedPools 是 routes 可直接引用的内置池名。
var reservedPools = map[string]bool{"direct": true, "default": true}

// validateProxyRouting 校验代理池与分流规则，让配置错误在启动时就暴露。
func (config *Config) validateProxyRouting() error {
	for name, urls := range config.Devin.ProxyPools {
		if trimmed := strings.TrimSpace(name); trimmed == "" || trimmed != name {
			return fmt.Errorf("devin.proxy_pools[%q]: pool name is empty or has surrounding spaces", name)
		}
		if reservedPools[name] {
			return fmt.Errorf("devin.proxy_pools[%q]: reserved pool name", name)
		}
		if len(urls) == 0 {
			return fmt.Errorf("devin.proxy_pools[%q]: pool has no addresses", name)
		}
		for _, raw := range urls {
			parsed, err := url.Parse(strings.TrimSpace(raw))
			if err != nil || parsed.Scheme == "" {
				return fmt.Errorf("devin.proxy_pools[%q]: invalid proxy URL %q", name, raw)
			}
			switch strings.ToLower(parsed.Scheme) {
			case "http", "https", "socks5", "socks5h":
			default:
				return fmt.Errorf("devin.proxy_pools[%q]: unsupported proxy scheme %q", name, parsed.Scheme)
			}
		}
	}
	for i, rule := range config.Devin.Routes {
		if len(rule.Models) == 0 {
			return fmt.Errorf("devin.routes[%d]: models must not be empty", i)
		}
		pool := strings.TrimSpace(rule.Pool)
		if pool == "" {
			return fmt.Errorf("devin.routes[%d]: pool is required", i)
		}
		if !reservedPools[pool] && config.Devin.ProxyPools[pool] == nil {
			return fmt.Errorf("devin.routes[%d]: unknown proxy pool %q", i, pool)
		}
		config.Devin.Routes[i].Pool = pool
	}
	return nil
}
