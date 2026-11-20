// 本文件负责加载配置、组装服务依赖并启动 HTTP 服务器。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leookun/devin-2api/internal/adapter"
	"github.com/leookun/devin-2api/internal/adapter/devin"
	"github.com/leookun/devin-2api/internal/app"
	"github.com/leookun/devin-2api/internal/config"
	"github.com/leookun/devin-2api/internal/dashboard"
	"github.com/leookun/devin-2api/internal/debuglog"
	"github.com/leookun/devin-2api/internal/httpproxy"
	"github.com/leookun/devin-2api/internal/stats"
)

func main() {
	configPath := flag.String("config", "config.yaml", "YAML 配置文件路径")
	flag.Parse()

	absoluteConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	serviceConfig, err := config.Load(absoluteConfigPath)
	if err != nil {
		log.Fatal(err)
	}

	providerAdapter := adapter.Adapter(adapter.Unavailable{Reason: "provider adapter is not configured"})
	if serviceConfig.Devin.Token != "" {
		configured, createErr := devin.New(devin.Config{
			BaseURL:     serviceConfig.Devin.BaseURL,
			Token:       serviceConfig.Devin.Token,
			Model:       serviceConfig.Devin.Model,
			Proxy:       serviceConfig.Devin.Proxy,
			ProxyPools:  proxyPoolsMap(serviceConfig.Devin.ProxyPools),
			Routes:      proxyRoutes(serviceConfig.Devin.Routes),
			ForceHTTP1:  serviceConfig.Devin.ForceHTTP1 != nil && *serviceConfig.Devin.ForceHTTP1,
			MaxNewlines: serviceConfig.Devin.MaxNewlines,
		})
		if createErr != nil {
			log.Fatal(createErr)
		}
		providerAdapter = configured
	}
	var debugManager *debuglog.Manager
	if serviceConfig.Debug.Enabled {
		debugManager = debuglog.NewManager(filepath.Join(filepath.Dir(absoluteConfigPath), "logs"))
	}
	// 请求统计：内存聚合，每 5 分钟增量落盘到配置文件同目录 stats.json，退出时再保存一次。
	statsCollector := stats.New(filepath.Join(filepath.Dir(absoluteConfigPath), "stats.json"))
	defer statsCollector.Close()
	application := app.New(providerAdapter, serviceConfig.Server, debugManager)
	application.SetAPIKey(serviceConfig.Auth.APIKey)
	application.SetStats(statsCollector)

	if proxy := strings.TrimSpace(serviceConfig.Devin.Proxy); proxy != "" {
		// 代理在启动时应用到全部上游请求（对话 + 面板数据），修改 config 后需重启生效。
		log.Printf("devin upstream proxy: %s", maskProxyUserinfo(proxy))
	}
	switch {
	case !serviceConfig.Dashboard.Enabled:
		if serviceConfig.Dashboard.Password != "" {
			log.Printf("dashboard.password is set but dashboard.enabled is off; /panel disabled")
		}
	case serviceConfig.Devin.Token == "":
		log.Printf("dashboard.enabled=true but devin.token is empty; /panel disabled")
	default:
		password, generated := resolveDashboardPassword(serviceConfig.Dashboard.Password)
		dashboardHandler := dashboard.New(password, serviceConfig.Devin.BaseURL, serviceConfig.Devin.Token, serviceConfig.Devin.Proxy, serviceConfig.Devin.ForceHTTP1 != nil && *serviceConfig.Devin.ForceHTTP1)
		dashboardHandler.SetStats(statsCollector)
		application.SetDashboard(dashboardHandler)
		switch {
		case generated:
			log.Printf("dashboard enabled at /panel, random password: %s (set dashboard.password to fix it, \"none\" for open access)", password)
		case password == "":
			log.Printf("dashboard enabled at /panel without password (dashboard.password=none)")
		default:
			log.Printf("dashboard enabled at /panel (password from config)")
		}
	}
	server := application.HTTPServer()
	log.Printf("HTTP server listening on %s", listenURL(server.Addr))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, server); err != nil {
		log.Fatal(err)
	}
}

// proxyPoolsMap 把 config 的 StringList 池映射为 httpproxy 需要的 []string。
func proxyPoolsMap(pools map[string]config.StringList) map[string][]string {
	if len(pools) == 0 {
		return nil
	}
	result := make(map[string][]string, len(pools))
	for name, urls := range pools {
		result[name] = []string(urls)
	}
	return result
}

// proxyRoutes 把 config 的 RouteRule 转换为 httpproxy.Route。
func proxyRoutes(rules []config.RouteRule) []httpproxy.Route {
	if len(rules) == 0 {
		return nil
	}
	routes := make([]httpproxy.Route, 0, len(rules))
	for _, rule := range rules {
		routes = append(routes, httpproxy.Route{Patterns: []string(rule.Models), Pool: rule.Pool})
	}
	return routes
}

// resolveDashboardPassword 解析面板密码配置：
// 留空 → 生成随机密码（generated=true，由调用方打印日志）；
// "none" → 返回空字符串，面板免密开放；
// 其他 → 固定密码原样返回。
func resolveDashboardPassword(password string) (resolved string, generated bool) {
	trimmed := strings.TrimSpace(password)
	if strings.EqualFold(trimmed, "none") {
		return "", false
	}
	if trimmed != "" {
		return trimmed, false
	}
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 不可用时退回固定默认密码，保证面板不至于完全无法访问。
		return "devin-panel", false
	}
	return hex.EncodeToString(buf), true
}

// maskProxyUserinfo 脱敏代理 URL 中的口令部分，避免把代理密码写入启动日志。
func maskProxyUserinfo(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		parsed.User = url.UserPassword(parsed.User.Username(), "hidden")
	}
	return parsed.String()
}

// listenURL 生成启动日志中的监听描述：配置为通配地址时同时给出 localhost 可访问地址。
func listenURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return listen + " (http://localhost:" + port + ")"
	}
	return "http://" + host + ":" + port
}

func run(ctx context.Context, server interface {
	ListenAndServe() error
	Shutdown(context.Context) error
	Close() error
}) error {
	result := make(chan error, 1)
	go func() {
		result <- server.ListenAndServe()
	}()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		// 优雅关闭超时（可能有活跃 SSE 流），强制关闭不再报错。
		server.Close()
	}
	return nil
}
