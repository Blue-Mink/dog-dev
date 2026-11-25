// Package trace 记录 Dog-dev 的结构化运行明细：请求 / 换源 / 流量。
// 与 Metrics 的聚合计数互补：Metrics 回答"多少"，trace 回答"哪次、哪个域名、哪个源"。
// 明细同时进内存环形缓冲（供 API 查询）与 JSONL 文件（重启后仍可查），
// Host 只存域名，不存路径、查询串或任何凭据。
package trace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Kind 是明细类型。
type Kind string

const (
	KindRequest  Kind = "request"  // 一次加速请求的最终结果
	KindFailover Kind = "failover" // 一次换源
	KindTransfer Kind = "transfer" // 一次成功转发的流量
)

// Event 是一条明细。字段按类型选用，未用的省略。
type Event struct {
	Seq      int64     `json:"seq"`
	Time     time.Time `json:"time"`
	Kind     Kind      `json:"kind"`
	Host     string    `json:"host"`
	Category string    `json:"category,omitempty"`
	Channel  string    `json:"channel,omitempty"` // direct / mirror / docker / pass
	Source   string    `json:"source,omitempty"`  // 命中的源名
	From     string    `json:"from,omitempty"`    // 换源：失败的源
	To       string    `json:"to,omitempty"`      // 换源：接管的新源
	Reason   string    `json:"reason,omitempty"`  // 换源原因
	Status   int       `json:"status,omitempty"`
	Bytes    int64     `json:"bytes,omitempty"`
	MS       int64     `json:"ms,omitempty"`
}

// Recorder 是有界环形缓冲 + JSONL 落盘的记录器。
type Recorder struct {
	mu    sync.RWMutex
	buf   []Event
	head  int
	count int
	cap   int
	seq   int64

	fileMu    sync.Mutex
	file      *os.File
	path      string
	maxBytes  int64
	keep      int
	fileBytes int64
}

// New 创建记录器。path 为空表示只留内存（测试用）。
// 启动时从 JSONL 读回最近 cap 条，实现重启后仍可查。
func New(path string, cap int, maxBytes int64, keep int) *Recorder {
	if cap <= 0 {
		cap = 2000
	}
	if keep < 1 {
		keep = 1
	}
	r := &Recorder{buf: make([]Event, cap), cap: cap, path: path, maxBytes: maxBytes, keep: keep}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			r.file, _ = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if r.file != nil {
				if fi, err := r.file.Stat(); err == nil {
					r.fileBytes = fi.Size()
				}
			}
		}
		r.loadDisk()
	}
	return r
}

// loadDisk 从 JSONL 读回最近 cap 条到内存环。坏行直接跳过。
func (r *Recorder) loadDisk() {
	f, err := os.Open(r.path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var all []Event
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if json.Unmarshal(line, &e) == nil {
			all = append(all, e)
		}
	}
	if len(all) > r.cap {
		all = all[len(all)-r.cap:]
	}
	for _, e := range all {
		r.buf[r.head] = e
		r.head = (r.head + 1) % r.cap
		r.count++
		if e.Seq > r.seq {
			r.seq = e.Seq
		}
	}
}

// Add 追加一条明细：先写内存环，再异步落盘（落盘失败不影响主流程）。
func (r *Recorder) Add(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	r.mu.Lock()
	r.seq++
	e.Seq = r.seq
	r.buf[r.head] = e
	r.head = (r.head + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
	r.mu.Unlock()

	r.appendDisk(e)
}

// Recent 返回最近 limit 条明细，可按 kind 过滤。结果按时间正序（旧的在前）。
func (r *Recorder) Recent(limit int, kind Kind) []Event {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Event
	n := r.count
	start := (r.head - n + r.cap) % r.cap
	for i := 0; i < n; i++ {
		e := r.buf[(start+i)%r.cap]
		if kind != "" && e.Kind != kind {
			continue
		}
		out = append(out, e)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// appendDisk 把一条明细追加到 JSONL，写满后轮转。
func (r *Recorder) appendDisk(e Event) {
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	if r.file == nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	if _, err = r.file.Write(b); err == nil {
		r.fileBytes += int64(len(b))
		if r.maxBytes > 0 && r.fileBytes >= r.maxBytes {
			r.rotate()
		}
	}
}

// rotate 执行 trace.jsonl → .1 → .2 … 的轮转（调用方持 fileMu）。
func (r *Recorder) rotate() {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(r.path+"."+strconv.Itoa(i), r.path+"."+strconv.Itoa(i+1))
	}
	_ = os.Rename(r.path, r.path+".1")
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		r.maxBytes = 0 // 磁盘只读：停落盘，内存与 API 不受影响
		return
	}
	r.file = f
	r.fileBytes = 0
}

// Close 关闭文件句柄。
func (r *Recorder) Close() error {
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		return err
	}
	return nil
}
