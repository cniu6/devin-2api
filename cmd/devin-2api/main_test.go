// 本文件验证服务入口能向调用方返回服务器启动失败。
package main

import (
	"context"
	"errors"
	"testing"
)

type fakeServer struct {
	shutdown bool
}

func (server *fakeServer) ListenAndServe() error {
	return errors.New("listen stopped")
}

func (server *fakeServer) Shutdown(context.Context) error {
	server.shutdown = true
	return nil
}

func (server *fakeServer) Close() error { return nil }

// TestListenURL verifies listen address descriptions used in the startup log.
func TestListenURL(t *testing.T) {
	cases := map[string]string{
		":8080":          ":8080 (http://localhost:8080)",
		"0.0.0.0:8080":   "0.0.0.0:8080 (http://localhost:8080)",
		"127.0.0.1:9090": "http://127.0.0.1:9090",
		"[::]:8080":      "[::]:8080 (http://localhost:8080)",
		"invalid":        "invalid",
	}
	for listen, want := range cases {
		if got := listenURL(listen); got != want {
			t.Errorf("listenURL(%q) = %q, want %q", listen, got, want)
		}
	}
}

// TestRunReturnsServeError verifies unexpected server failures are returned to main.
func TestRunReturnsServeError(t *testing.T) {
	server := &fakeServer{}
	if err := run(context.Background(), server); err == nil {
		t.Fatal("run() error = nil, want serve error")
	}
	if server.shutdown {
		t.Fatal("server was shut down after an unexpected serve failure")
	}
}

// TestResolveDashboardPassword 验证面板密码三态：随机 / none 免密 / 固定。
func TestResolveDashboardPassword(t *testing.T) {
	fixed, generated := resolveDashboardPassword("s3cret")
	if generated || fixed != "s3cret" {
		t.Fatalf("resolveDashboardPassword(s3cret) = %q, %v; want fixed password", fixed, generated)
	}
	open, generated := resolveDashboardPassword("none")
	if generated || open != "" {
		t.Fatalf("resolveDashboardPassword(none) = %q, %v; want open access", open, generated)
	}
	random, generated := resolveDashboardPassword("")
	if !generated || len(random) != 18 {
		t.Fatalf("resolveDashboardPassword(\"\") = %q, %v; want 18-char random password", random, generated)
	}
	again, _ := resolveDashboardPassword("  ")
	if again == random {
		t.Fatal("two random passwords should differ")
	}
}

// TestMaskProxyUserinfo 验证启动日志不泄露代理口令。
func TestMaskProxyUserinfo(t *testing.T) {
	cases := map[string]string{
		"socks5://127.0.0.1:1080":         "socks5://127.0.0.1:1080",
		"http://user:pass@127.0.0.1:7890": "http://user:hidden@127.0.0.1:7890",
		"http://user@127.0.0.1:7890":      "http://user@127.0.0.1:7890",
		"not a url":                       "not a url",
	}
	for raw, want := range cases {
		if got := maskProxyUserinfo(raw); got != want {
			t.Errorf("maskProxyUserinfo(%q) = %q, want %q", raw, got, want)
		}
	}
}
