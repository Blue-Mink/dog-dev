package trace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecentFilterAndOrder(t *testing.T) {
	r := New("", 10, 0, 1)
	r.Add(Event{Kind: KindRequest, Host: "github.com"})
	r.Add(Event{Kind: KindFailover, Host: "raw.githubusercontent.com", From: "A", To: "B"})
	r.Add(Event{Kind: KindRequest, Host: "codeload.github.com"})
	r.Add(Event{Kind: KindTransfer, Host: "github.com", Bytes: 1234})

	all := r.Recent(0, "")
	if len(all) != 4 {
		t.Fatalf("want 4 events, got %d", len(all))
	}
	// 时间正序：seq 递增
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("seq not increasing at %d: %d <= %d", i, all[i].Seq, all[i-1].Seq)
		}
	}
	req := r.Recent(0, KindRequest)
	if len(req) != 2 {
		t.Fatalf("want 2 requests, got %d", len(req))
	}
	fo := r.Recent(1, KindFailover)
	if len(fo) != 1 || fo[0].From != "A" || fo[0].To != "B" {
		t.Fatalf("failover filter wrong: %+v", fo)
	}
}

func TestRingEvictsOldest(t *testing.T) {
	r := New("", 3, 0, 1)
	for i := 0; i < 5; i++ {
		r.Add(Event{Kind: KindRequest, Host: "h" + string(rune('0'+i))})
	}
	all := r.Recent(0, "")
	if len(all) != 3 {
		t.Fatalf("want 3 (cap), got %d", len(all))
	}
	// 环形缓冲淘汰最旧：剩 h2/h3/h4
	if all[0].Host != "h2" || all[2].Host != "h4" {
		t.Fatalf("ring eviction wrong: %+v", all)
	}
}

func TestDiskPersistAndReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trace.jsonl")
	r := New(p, 100, 0, 1)
	r.Add(Event{Kind: KindFailover, Host: "github.com", From: "源A", To: "源B", Reason: "超时"})
	r.Add(Event{Kind: KindTransfer, Host: "github.com", Bytes: 999})
	r.Close()

	r2 := New(p, 100, 0, 1)
	defer r2.Close()
	all := r2.Recent(0, "")
	if len(all) != 2 {
		t.Fatalf("reload want 2, got %d", len(all))
	}
	if all[0].Host != "github.com" || all[0].Seq != 1 {
		t.Fatalf("reload first event wrong: %+v", all[0])
	}
	if all[1].Bytes != 999 {
		t.Fatalf("reload bytes wrong: %+v", all[1])
	}
	// 新写入序号接着磁盘最大值
	r2.Add(Event{Kind: KindRequest, Host: "x"})
	last := r2.Recent(1, "")[0]
	if last.Seq != 3 {
		t.Fatalf("seq after reload want 3, got %d", last.Seq)
	}
}

func TestRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trace.jsonl")
	// 每条约 100 字节，阈值 250 触发至少一次轮转，保留 2 份
	r := New(p, 100, 250, 2)
	for i := 0; i < 10; i++ {
		r.Add(Event{Kind: KindRequest, Host: "github.com", Bytes: int64(i)})
	}
	r.Close()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("current file missing: %v", err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("rotated .1 missing: %v", err)
	}
	// 内存环仍是全部 10 条
	if got := len(r.Recent(0, "")); got != 10 {
		t.Fatalf("mem ring want 10, got %d", got)
	}
	_ = time.Now()
}
