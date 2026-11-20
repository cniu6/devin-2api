// 本文件验证命名代理池、轮询分摊与通配符分流规则的行为。
package httpproxy

import (
	"net/http"
	"testing"
)

func directTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	transport, err := NewTransport("", true)
	if err != nil {
		t.Fatalf("NewTransport direct: %v", err)
	}
	return transport
}

// TestPoolForFirstMatchWins 验证规则自上而下首个命中生效。
func TestPoolForFirstMatchWins(t *testing.T) {
	router, err := NewRouter(
		map[string][]string{
			"us": {"http://127.0.0.1:8081"},
			"hk": {"http://127.0.0.1:8082"},
		},
		[]Route{
			{Patterns: []string{"swe-*"}, Pool: "us"},
			{Patterns: []string{"*"}, Pool: "hk"},
		},
		directTransport(t), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := router.PoolFor("swe-2-high"); got != "us" {
		t.Fatalf("PoolFor swe-2-high = %q, want us", got)
	}
	if got := router.PoolFor("glm-5-3-low"); got != "hk" {
		t.Fatalf("PoolFor glm-5-3-low = %q, want hk", got)
	}
}

// TestPoolForCaseInsensitiveAndIDs 验证大小写不敏感及按 uid/标签/系列/分组任一标识匹配。
func TestPoolForCaseInsensitiveAndIDs(t *testing.T) {
	router, err := NewRouter(
		map[string][]string{"us": {"http://127.0.0.1:8081"}},
		[]Route{
			{Patterns: []string{"SWE-*"}, Pool: "us"},
			{Patterns: []string{"Moonshot"}, Pool: "us"},
		},
		directTransport(t), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := router.PoolFor("swe-2-high"); got != "us" {
		t.Fatalf("PoolFor uid = %q, want us", got)
	}
	// uid 不命中，但传入的分组名命中第二条规则
	if got := router.PoolFor("kimi-k3-low", "Kimi K3 Low", "Kimi K3", "Moonshot"); got != "us" {
		t.Fatalf("PoolFor group = %q, want us", got)
	}
	if got := router.PoolFor("glm-5-3-low", "GLM-5.3 Low"); got != "default" {
		t.Fatalf("PoolFor unmatched = %q, want default", got)
	}
}

// TestPoolForQuestionMarkAndAnchored 验证 ? 单字符通配与整体锚定（非子串匹配）。
func TestPoolForQuestionMarkAndAnchored(t *testing.T) {
	router, err := NewRouter(
		map[string][]string{"us": {"http://127.0.0.1:8081"}},
		[]Route{
			{Patterns: []string{"swe-?"}, Pool: "us"},
			{Patterns: []string{"glm"}, Pool: "direct"},
		},
		directTransport(t), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := router.PoolFor("swe-2"); got != "us" {
		t.Fatalf("PoolFor swe-2 = %q, want us", got)
	}
	if got := router.PoolFor("swe-22"); got != "default" {
		t.Fatalf("PoolFor swe-22 = %q, want default (? only matches one char)", got)
	}
	// "glm" 规则整体锚定，"glm-5" 是更长的串，不应被当成子串命中
	if got := router.PoolFor("glm-5"); got != "default" {
		t.Fatalf("PoolFor glm-5 = %q, want default (anchored match)", got)
	}
	if got := router.PoolFor("glm"); got != "direct" {
		t.Fatalf("PoolFor glm = %q, want direct", got)
	}
}

// TestTransportForRoundRobin 验证池内多地址按请求轮询分摊。
func TestTransportForRoundRobin(t *testing.T) {
	router, err := NewRouter(
		map[string][]string{
			"us": {"http://127.0.0.1:8081", "http://127.0.0.1:8082"},
		},
		[]Route{{Patterns: []string{"*"}, Pool: "us"}},
		directTransport(t), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	first := router.TransportFor("m1")
	second := router.TransportFor("m2")
	third := router.TransportFor("m3")
	if first == second {
		t.Fatal("round-robin: consecutive transports identical, want alternation")
	}
	if first != third {
		t.Fatal("round-robin: third transport differs from first, want wrap-around")
	}
}

// TestTransportForFallback 验证未命中规则时返回 fallback transport。
func TestTransportForFallback(t *testing.T) {
	fallback := directTransport(t)
	router, err := NewRouter(
		map[string][]string{"us": {"http://127.0.0.1:8081"}},
		[]Route{{Patterns: []string{"swe-*"}, Pool: "us"}},
		fallback, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := router.TransportFor("glm-5"); got != fallback {
		t.Fatal("unmatched model should use fallback transport")
	}
	// 显式 direct 池返回直连 transport（非 fallback 同一对象时也可路由）
	if got := router.PoolFor("nothing"); got != "default" {
		t.Fatalf("PoolFor = %q, want default", got)
	}
}

// TestNewRouterValidation 验证非法池/规则在构建期报错。
func TestNewRouterValidation(t *testing.T) {
	fallback := directTransport(t)
	cases := []struct {
		name  string
		pools map[string][]string
		specs []Route
	}{
		{"reserved name", map[string][]string{"direct": {"http://127.0.0.1:1"}}, nil},
		{"reserved default", map[string][]string{"default": {"http://127.0.0.1:1"}}, nil},
		{"empty pool", map[string][]string{"us": {}}, nil},
		{"bad url", map[string][]string{"us": {"://bad"}}, nil},
		{"unsupported scheme", map[string][]string{"us": {"ftp://127.0.0.1:1"}}, nil},
		{"spaced name", map[string][]string{" us": {"http://127.0.0.1:1"}}, nil},
		{"unknown route pool", nil, []Route{{Patterns: []string{"*"}, Pool: "nope"}}},
		{"empty pattern", nil, []Route{{Patterns: []string{""}, Pool: "direct"}}},
	}
	for _, tc := range cases {
		if _, err := NewRouter(tc.pools, tc.specs, fallback, true); err == nil {
			t.Fatalf("%s: NewRouter error = nil, want error", tc.name)
		}
	}
}

// TestNewRouterEmpty 验证无池无规则时全部走 fallback。
func TestNewRouterEmpty(t *testing.T) {
	fallback := directTransport(t)
	router, err := NewRouter(nil, nil, fallback, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := router.PoolFor("anything"); got != "default" {
		t.Fatalf("PoolFor = %q, want default", got)
	}
	if got := router.TransportFor("anything"); got != fallback {
		t.Fatal("TransportFor should return fallback")
	}
}
