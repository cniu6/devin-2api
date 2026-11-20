// Package stats 收集网关请求的内存统计并按周期持久化到 stats.json。
// 数据用于 /panel/api/stats：总量计数、按小时分桶、按模型聚合、最近请求记录。
package stats

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	// recentLimit 是「最近请求」记录保留条数。
	recentLimit = 20
	// flushInterval 是周期落盘间隔；仅在有新数据（dirty）时写。
	flushInterval = 5 * time.Minute
	// bucketKeepHours 是按小时分桶保留时长。
	bucketKeepHours = 48
	// bucketShowHours 是快照中返回的最近桶数。
	bucketShowHours = 24
)

// maxBodyCapture 是请求/响应详情在内存中保留的最大字节数，超出截断。
const maxBodyCapture = 48 << 10

// Entry 是一次请求的统计记录。
// ReqHeaders/ReqBody/RespBody 为面板「最近请求」详情，仅保留在内存中，不落盘。
type Entry struct {
	Time       time.Time `json:"time"`
	Path       string    `json:"path"`
	IP         string    `json:"ip"`
	Model      string    `json:"model"`
	Result     string    `json:"result"` // completed / failed / disconnected
	Status     int       `json:"status"`
	DurationMS int64     `json:"duration_ms"`
	Input      int64     `json:"input"`
	Output     int64     `json:"output"`
	CacheRead  int64     `json:"cache_read"`
	CacheWrite int64     `json:"cache_write"`
	Total      int64     `json:"total"`
	// 请求/响应详情（已脱敏、截断），仅内存展示用；持久化时丢弃。
	ReqHeaders map[string]string `json:"req_headers,omitempty"`
	ReqBody    string            `json:"req_body,omitempty"`
	RespBody   string            `json:"resp_body,omitempty"`
}

// TruncateBody 截断超长请求/响应体到 maxBodyCapture，尾部附截断标记。
func TruncateBody(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > maxBodyCapture {
		return string(b[:maxBodyCapture]) + "\n… (truncated)"
	}
	return string(b)
}

// Bucket 是一小时聚合桶。
type Bucket struct {
	Hour     int64 `json:"hour"`
	Requests int64 `json:"requests"`
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
}

// Totals 是累计计数。
type Totals struct {
	Requests     int64 `json:"requests"`
	Completed    int64 `json:"completed"`
	Failed       int64 `json:"failed"`
	Disconnected int64 `json:"disconnected"`
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite   int64 `json:"cache_write"`
	TotalTokens  int64 `json:"total_tokens"`
	DurationMS   int64 `json:"duration_ms"`
}

// ModelStat 是单模型聚合。
type ModelStat struct {
	Requests int64 `json:"requests"`
	Total    int64 `json:"total"`
}

// Snapshot 是 /panel/api/stats 的响应负载。
type Snapshot struct {
	StartedAt   time.Time            `json:"started_at"`
	UptimeSec   int64                `json:"uptime_sec"`
	Totals      Totals               `json:"totals"`
	SuccessRate float64              `json:"success_rate"`
	AvgMS       int64                `json:"avg_ms"`
	Buckets     []Bucket             `json:"buckets"`
	Models      map[string]ModelStat `json:"models"`
	Recent      []Entry              `json:"recent"`
}

// persistedFile 是 stats.json 的落盘结构。
type persistedFile struct {
	SavedAt time.Time            `json:"saved_at"`
	Totals  Totals               `json:"totals"`
	Buckets []Bucket             `json:"buckets"`
	Models  map[string]ModelStat `json:"models"`
	Recent  []Entry              `json:"recent"`
}

// Collector 是线程安全的内存统计器；后台按固定间隔把脏数据落盘。
type Collector struct {
	mu        sync.Mutex
	filePath  string
	startedAt time.Time
	totals    Totals
	buckets   map[int64]Bucket
	models    map[string]ModelStat
	recent    []Entry
	dirty     bool
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// New 创建统计器并从 filePath 恢复上次持久化的数据（不存在则从头开始）。
// 启动后台协程每 flushInterval 检查一次脏标记并保存。
func New(filePath string) *Collector {
	c := &Collector{
		filePath:  filePath,
		startedAt: time.Now(),
		buckets:   make(map[int64]Bucket),
		models:    make(map[string]ModelStat),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	c.load()
	go c.flushLoop()
	return c
}

// Record 记录一次请求结果。
func (c *Collector) Record(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totals.Requests++
	c.totals.DurationMS += e.DurationMS
	switch e.Result {
	case "completed":
		c.totals.Completed++
	case "disconnected":
		c.totals.Disconnected++
	default:
		c.totals.Failed++
	}
	c.totals.Input += e.Input
	c.totals.Output += e.Output
	c.totals.CacheRead += e.CacheRead
	c.totals.CacheWrite += e.CacheWrite
	c.totals.TotalTokens += e.Total

	hour := e.Time.Truncate(time.Hour).Unix()
	bucket := c.buckets[hour]
	bucket.Hour = hour
	bucket.Requests++
	bucket.Input += e.Input
	bucket.Output += e.Output
	bucket.Cached += e.CacheRead + e.CacheWrite
	c.buckets[hour] = bucket

	if e.Model != "" {
		stat := c.models[e.Model]
		stat.Requests++
		stat.Total += e.Total
		c.models[e.Model] = stat
	}

	c.recent = append([]Entry{e}, c.recent...)
	if len(c.recent) > recentLimit {
		c.recent = c.recent[:recentLimit]
	}
	c.dirty = true
}

// Snapshot 返回当前统计的可序列化快照（recent 按时间倒序，buckets 最近 24 小时）。
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := Snapshot{
		StartedAt: c.startedAt,
		UptimeSec: int64(time.Since(c.startedAt).Seconds()),
		Totals:    c.totals,
		Models:    make(map[string]ModelStat, len(c.models)),
		Recent:    append([]Entry(nil), c.recent...),
	}
	for k, v := range c.models {
		snap.Models[k] = v
	}
	if c.totals.Requests > 0 {
		snap.SuccessRate = float64(c.totals.Completed) / float64(c.totals.Requests)
		snap.AvgMS = c.totals.DurationMS / c.totals.Requests
	}
	cutoff := time.Now().Add(-bucketShowHours * time.Hour).Truncate(time.Hour).Unix()
	snap.Buckets = make([]Bucket, 0, len(c.buckets))
	for hour, bucket := range c.buckets {
		if hour >= cutoff {
			snap.Buckets = append(snap.Buckets, bucket)
		}
	}
	sort.Slice(snap.Buckets, func(i, j int) bool { return snap.Buckets[i].Hour < snap.Buckets[j].Hour })
	return snap
}

// Save 立即把当前数据写入磁盘（临时文件 + 原子改名）。
func (c *Collector) Save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saveLocked()
}

// Close 停止后台协程并做最终落盘。
func (c *Collector) Close() {
	close(c.stopCh)
	<-c.doneCh
	c.Save()
}

func (c *Collector) flushLoop() {
	defer close(c.doneCh)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			if c.dirty {
				c.saveLocked()
			}
			c.mu.Unlock()
		case <-c.stopCh:
			return
		}
	}
}

func (c *Collector) saveLocked() {
	payload := persistedFile{
		SavedAt: time.Now(),
		Totals:  c.totals,
		Models:  c.models,
	}
	// 详情字段（请求头/请求体/响应体）只在内存中保留，不落盘。
	payload.Recent = make([]Entry, len(c.recent))
	for i, e := range c.recent {
		e.ReqHeaders = nil
		e.ReqBody = ""
		e.RespBody = ""
		payload.Recent[i] = e
	}
	cutoff := time.Now().Add(-bucketKeepHours * time.Hour).Truncate(time.Hour).Unix()
	payload.Buckets = make([]Bucket, 0, len(c.buckets))
	for hour, bucket := range c.buckets {
		if hour >= cutoff {
			payload.Buckets = append(payload.Buckets, bucket)
		}
	}
	sort.Slice(payload.Buckets, func(i, j int) bool { return payload.Buckets[i].Hour < payload.Buckets[j].Hour })
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(c.filePath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := c.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("stats: save %s: %v", c.filePath, err)
		return
	}
	if err := os.Rename(tmp, c.filePath); err != nil {
		log.Printf("stats: rename %s: %v", tmp, err)
		return
	}
	c.dirty = false
}

func (c *Collector) load() {
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("stats: load %s: %v", c.filePath, err)
		}
		return
	}
	var payload persistedFile
	if err := json.Unmarshal(data, &payload); err != nil {
		log.Printf("stats: parse %s: %v", c.filePath, err)
		return
	}
	c.totals = payload.Totals
	if payload.Models != nil {
		c.models = payload.Models
	}
	c.recent = payload.Recent
	if len(c.recent) > recentLimit {
		c.recent = c.recent[:recentLimit]
	}
	for _, bucket := range payload.Buckets {
		c.buckets[bucket.Hour] = bucket
	}
}
