package app

import (
	"testing"
	"time"
)

// 托管 IP 复检的处置逻辑（真实 HTTP 校验依赖网络，这里只钉住计数与调度）。
func TestHostHealthVerdictFor(t *testing.T) {
	fail := map[string]int{}

	// 第一轮：一个域名失败 → 不重选，但下一轮提前到 1 分钟。
	v := hostHealthVerdictFor(fail, map[string]bool{"github.com": false, "raw.githubusercontent.com": true}, 2)
	if v.Resync {
		t.Fatalf("首轮失败不该立即重选: %+v", v)
	}
	if v.Next != hostsHealthRetry {
		t.Fatalf("首轮失败应把下一轮提前到 %v，实际 %v", hostsHealthRetry, v.Next)
	}
	if fail["github.com"] != 1 {
		t.Fatalf("失败计数应为 1，实际 %d", fail["github.com"])
	}

	// 第二轮仍失败 → 达到阈值，立即重选。
	v = hostHealthVerdictFor(fail, map[string]bool{"github.com": false}, 2)
	if !v.Resync {
		t.Fatalf("连续两轮失败应触发重选: %+v", v)
	}

	// 恢复后计数清零，且不再加速观察。
	v = hostHealthVerdictFor(fail, map[string]bool{"github.com": true}, 2)
	if v.Resync || v.Next != 0 {
		t.Fatalf("恢复后应既不重选也不提前: %+v", v)
	}
	if _, ok := fail["github.com"]; ok {
		t.Fatalf("恢复后应清除失败计数: %v", fail)
	}

	// 全绿时无动作。
	if v = hostHealthVerdictFor(fail, map[string]bool{"github.com": true}, 2); v.Resync || v.Next != 0 {
		t.Fatalf("全绿不该有任何处置: %+v", v)
	}
}

// 已退出托管区的域名不能保留旧计数，否则下次接管时会被秒判死。
func TestHostHealthVerdictDropsStaleHost(t *testing.T) {
	fail := map[string]int{"codeload.github.com": 5}
	v := hostHealthVerdictFor(fail, map[string]bool{"github.com": true}, 2)
	if v.Resync || v.Next != 0 {
		t.Fatalf("不该触发处置: %+v", v)
	}
	if len(fail) != 0 {
		t.Fatalf("陈旧计数应被清除，实际 %v", fail)
	}
}

// 抖动一次（同一轮内补一次机会成功）不应累计死亡计数。
func TestHostHealthVerdictSingleSuccessClears(t *testing.T) {
	fail := map[string]int{"api.github.com": 1}
	v := hostHealthVerdictFor(fail, map[string]bool{"api.github.com": true}, 2)
	if v.Resync {
		t.Fatalf("成功后不该重选: %+v", v)
	}
	if fail["api.github.com"] != 0 {
		t.Fatalf("成功后计数应清零，实际 %d", fail["api.github.com"])
	}
	// 复检节拍必须短于 hosts 刷新周期（默认 60 分钟），否则等同于没做。
	if hostsHealthInterval >= 60*time.Minute {
		t.Fatalf("复检节拍 %v 太长", hostsHealthInterval)
	}
}
