package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectorRecordAndSnapshot(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "stats.json"))
	defer c.Close()

	c.Record(Entry{Path: "/v1/messages", Model: "swe-2-high", Result: "completed", Status: 200, DurationMS: 120, Input: 10, Output: 5, CacheRead: 2, CacheWrite: 3, Total: 15})
	c.Record(Entry{Path: "/v1/chat/completions", Model: "swe-2-high", Result: "failed", Status: 502, DurationMS: 60})
	c.Record(Entry{Path: "/v1/responses", Model: "gpt-6-medium", Result: "disconnected", Status: 200, DurationMS: 30, Output: 1, Total: 1})

	snap := c.Snapshot()
	if snap.Totals.Requests != 3 || snap.Totals.Completed != 1 || snap.Totals.Failed != 1 || snap.Totals.Disconnected != 1 {
		t.Fatalf("unexpected totals: %+v", snap.Totals)
	}
	if snap.Totals.Input != 10 || snap.Totals.Output != 6 || snap.Totals.CacheRead != 2 || snap.Totals.CacheWrite != 3 || snap.Totals.TotalTokens != 16 {
		t.Fatalf("unexpected token totals: %+v", snap.Totals)
	}
	if snap.AvgMS != 70 {
		t.Fatalf("avg duration = %d, want 70", snap.AvgMS)
	}
	if len(snap.Recent) != 3 || snap.Recent[0].Path != "/v1/responses" {
		t.Fatalf("recent order wrong: %+v", snap.Recent)
	}
	if len(snap.Buckets) != 1 || snap.Buckets[0].Requests != 3 || snap.Buckets[0].Cached != 5 {
		t.Fatalf("unexpected buckets: %+v", snap.Buckets)
	}
	if snap.Models["swe-2-high"].Requests != 2 || snap.Models["gpt-6-medium"].Requests != 1 {
		t.Fatalf("unexpected model stats: %+v", snap.Models)
	}
}

func TestCollectorRecentLimit(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "stats.json"))
	defer c.Close()
	for i := 0; i < 25; i++ {
		c.Record(Entry{Path: "/x", Result: "completed"})
	}
	snap := c.Snapshot()
	if len(snap.Recent) != recentLimit {
		t.Fatalf("recent len = %d, want %d", len(snap.Recent), recentLimit)
	}
}

func TestCollectorPersistence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "stats.json")
	c := New(file)
	c.Record(Entry{Path: "/v1/messages", Model: "m1", Result: "completed", Status: 200, Input: 7, Output: 3, Total: 10,
		ReqHeaders: map[string]string{"Content-Type": "application/json"}, ReqBody: `{"a":1}`, RespBody: `{"b":2}`})
	c.Close()
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("stats file not written: %v", err)
	}
	data, _ := os.ReadFile(file)
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("stats file not json: %v", err)
	}
	// 详情字段不落盘。
	if raw := string(data); strings.Contains(raw, "req_body") || strings.Contains(raw, "req_headers") || strings.Contains(raw, "resp_body") {
		t.Fatalf("stats file leaked detail fields: %s", raw)
	}
	c2 := New(file)
	defer c2.Close()
	snap := c2.Snapshot()
	if snap.Totals.Requests != 1 || snap.Totals.Input != 7 || snap.Models["m1"].Requests != 1 {
		t.Fatalf("restored totals wrong: %+v", snap.Totals)
	}
}

func TestCollectorLoadMissingFile(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "missing", "stats.json"))
	defer c.Close()
	c.Record(Entry{Result: "completed"})
	snap := c.Snapshot()
	if snap.Totals.Requests != 1 || snap.UptimeSec < 0 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	// 目录不存在时保存应自动创建。
	c.Save()
	if _, err := os.Stat(c.filePath); err != nil {
		t.Fatalf("save did not create file: %v", err)
	}
	_ = time.Now() // keep import used if assertions change
}
