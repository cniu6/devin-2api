// 本文件实现命名代理池与按模型分流的 Router：
// 配置层把模型 uid / 标签 / 系列 / 分组映射到动态命名的代理池，
// 池内多个代理地址按请求轮询分摊，保证高并发下单代理不被打满。
package httpproxy

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
)

// Route 描述一条分流规则：命中 Patterns 中任一通配符即把请求发往 Pool 代理池。
type Route struct {
	Patterns []string
	Pool     string
}

// compiledRoute 是预编译后的分流规则。
type compiledRoute struct {
	patterns []*regexp.Regexp
	pool     string
}

// pool 是一组同用途代理地址；每个地址一个独立 Transport，请求按轮询分摊。
type pool struct {
	name       string
	transports []http.RoundTripper
	counter    atomic.Uint64
}

func (p *pool) next() http.RoundTripper {
	return p.transports[int(p.counter.Add(1)-1)%len(p.transports)]
}

// Router 根据模型标识（uid / 标签 / 系列 / 分组）选择上游 Transport；
// 未命中任何规则时返回 fallback（即 devin.proxy 对应的 Transport，留空时为直连）。
type Router struct {
	pools    map[string]*pool
	rules    []compiledRoute
	fallback http.RoundTripper
}

// NewRouter 构建代理池与分流规则。
// pools 为动态命名的代理地址集合；内置池名：direct（直连/系统环境变量代理）、
// default（= fallback，即 devin.proxy）。specs 从上到下匹配，第一条命中的规则生效。
func NewRouter(pools map[string][]string, specs []Route, fallback http.RoundTripper, forceHTTP1 bool) (*Router, error) {
	direct, err := NewTransport("", forceHTTP1)
	if err != nil {
		return nil, fmt.Errorf("build direct transport: %w", err)
	}
	router := &Router{
		pools: map[string]*pool{
			"direct":  {name: "direct", transports: []http.RoundTripper{direct}},
			"default": {name: "default", transports: []http.RoundTripper{fallback}},
		},
		fallback: fallback,
	}
	for name, urls := range pools {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || trimmed != name {
			return nil, fmt.Errorf("proxy pool name %q is empty or has surrounding spaces", name)
		}
		if _, reserved := router.pools[name]; reserved {
			return nil, fmt.Errorf("proxy pool name %q is reserved", name)
		}
		if len(urls) == 0 {
			return nil, fmt.Errorf("proxy pool %q has no addresses", name)
		}
		transports := make([]http.RoundTripper, 0, len(urls))
		for _, raw := range urls {
			transport, err := NewTransport(raw, forceHTTP1)
			if err != nil {
				return nil, fmt.Errorf("proxy pool %q address %q: %w", name, raw, err)
			}
			transports = append(transports, transport)
		}
		router.pools[name] = &pool{name: name, transports: transports}
	}
	for i, spec := range specs {
		if _, ok := router.pools[spec.Pool]; !ok {
			return nil, fmt.Errorf("route %d references unknown proxy pool %q", i, spec.Pool)
		}
		compiled := compiledRoute{pool: spec.Pool}
		for _, pattern := range spec.Patterns {
			re, err := globRegexp(pattern)
			if err != nil {
				return nil, fmt.Errorf("route %d pattern %q: %w", i, pattern, err)
			}
			compiled.patterns = append(compiled.patterns, re)
		}
		router.rules = append(router.rules, compiled)
	}
	return router, nil
}

// TransportFor 返回首个命中规则的代理池 Transport；未命中返回 fallback。
// ids 按 uid → 标签 → 系列 → 分组的顺序传入即可。
func (router *Router) TransportFor(ids ...string) http.RoundTripper {
	poolName := router.PoolFor(ids...)
	return router.pools[poolName].next()
}

// PoolFor 返回首个命中规则的代理池名；未命中返回 "default"（fallback）。
func (router *Router) PoolFor(ids ...string) string {
	for _, rule := range router.rules {
		for _, pattern := range rule.patterns {
			for _, id := range ids {
				if id != "" && pattern.MatchString(id) {
					return rule.pool
				}
			}
		}
	}
	return "default"
}

// globRegexp 把通配符模式编译成正则：* 任意串、? 单字符，忽略大小写，整体锚定。
func globRegexp(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
