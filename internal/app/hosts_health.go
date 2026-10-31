package app

import (
	"context"
	"time"

	"github.com/ghpp/ghpp/internal/config"
)

// 托管 IP 健康复检（2026-10-31 用户令：hosts 里的死 IP 不能钉住一小时）。
//
// 背景：hosts 条目只在写盘前校验一次，之后要等 refresh_minutes（默认 60 分钟）
// 才重新优选。节点抖动时（实测 20.27.177.113 由 302/0.42s 掉到 25–30s 超时）整机
// github.com 会被打进黑洞，最长持续一小时。这里补一个轻量复检回路：
//   - 每 5 分钟对「当前托管区」的每条记录做一次 HTTP 校验（同一轮给两次机会，
//     抖动的节点常见「一次失败、下一次就通」，避免把偶发抖动判死）；
//   - 连续 2 轮失败 → 立即重新优选并改写 hosts；
//   - 有域名失败但未达阈值 → 把下一次复检提前到 1 分钟后。
//
// 只读托管区、只在判定失败时改自己写的那几条，不碰用户在 hosts 里的其它条目。
const (
	hostsHealthInterval  = 5 * time.Minute
	hostsHealthFailLimit = 2
	hostsHealthRetry     = time.Minute
)

// hostHealthVerdict 是本轮复检的处置结论。
type hostHealthVerdict struct {
	// Resync 表示有域名连续失败已达阈值，需要立即重新优选。
	Resync bool
	// Next 非零表示下一轮复检应提前到该间隔之后（抖动期加速观察）。
	Next time.Duration
}

// hostHealthVerdictFor 依据本轮校验结果更新按域名的连续失败计数。
//
// 纯函数：便于单测（真实校验依赖网络，不适合在单测里跑）。
func hostHealthVerdictFor(fail map[string]int, results map[string]bool, limit int) hostHealthVerdict {
	v := hostHealthVerdict{}
	for host, ok := range results {
		if ok {
			delete(fail, host)
			continue
		}
		fail[host]++
		if limit > 0 && fail[host] >= limit {
			v.Resync = true
		}
		v.Next = hostsHealthRetry
	}
	// 已不在托管区的域名不保留历史计数，避免下次重新接管时被旧计数秒判死。
	for host := range fail {
		if _, live := results[host]; !live {
			delete(fail, host)
		}
	}
	return v
}

// tickHostsHealth 按 5 分钟节拍驱动托管 IP 健康复检（在线程上不阻塞主循环）。
func (a *App) tickHostsHealth(ctx context.Context) {
	cfg := a.CurrentConfig()
	if !cfg.Hosts.Enabled || a.optimize == nil || a.hostsMgr == nil {
		return
	}
	a.mu.Lock()
	if a.hostsHealing || time.Since(a.lastHostsHealth) < hostsHealthInterval {
		a.mu.Unlock()
		return
	}
	a.lastHostsHealth = time.Now()
	a.hostsHealing = true
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.hostsHealing = false
			a.mu.Unlock()
		}()
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		a.checkHostsHealth(cctx, cfg)
	}()
}

// checkHostsHealth 校验托管区里每条记录的 IP 是否仍然可用。
func (a *App) checkHostsHealth(ctx context.Context, cfg *config.Config) {
	entries, err := a.hostsMgr.Current()
	if err != nil {
		a.log.Logf("warn", "托管 IP 复检读取 hosts 失败: %v", err)
		return
	}
	if len(entries) == 0 {
		return
	}

	paths := make(map[string]string, len(entries))
	for _, t := range config.DomainProbeTargets() {
		paths[t.Host] = t.Path
	}
	timeout := time.Duration(cfg.Hosts.ProbeTimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 8*time.Second {
		timeout = 8 * time.Second
	}
	check := a.httpCheck
	if check == nil {
		check = a.optimize.HTTPCheck
	}

	results := make(map[string]bool, len(entries))
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		ok := check(ctx, e.Host, e.IP, paths[e.Host], timeout)
		if !ok {
			// 抖动节点常见「一次失败、下一次就通」，同轮补一次机会。
			ok = check(ctx, e.Host, e.IP, paths[e.Host], timeout)
		}
		results[e.Host] = ok
		if !ok {
			a.log.Logf("warn", "托管 IP 复检失败: %s → %s", e.Host, e.IP)
		}
	}

	a.mu.Lock()
	if a.hostsFail == nil {
		a.hostsFail = make(map[string]int, len(results))
	}
	verdict := hostHealthVerdictFor(a.hostsFail, results, hostsHealthFailLimit)
	if verdict.Resync {
		a.hostsFail = make(map[string]int, len(results))
	}
	a.mu.Unlock()

	if verdict.Resync {
		a.log.Logf("warn", "托管 IP 连续 %d 轮复检失败，立即重新优选并改写 hosts", hostsHealthFailLimit)
		// 重选给独立预算：DNS 优选 + 写盘前的 HTTP 校验在坏网络下可能远超复检
		// 自己的 3 分钟（1.3.11 实测：ctx 被截断后 github.com 反而没被写回，
		// 只能等 30 分钟一次的全量刷新，等于白修）。
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Minute)
		defer cancel()
		a.SyncHosts(rctx)
		return
	}
	if verdict.Next > 0 {
		a.mu.Lock()
		a.lastHostsHealth = time.Now().Add(-(hostsHealthInterval - verdict.Next))
		a.mu.Unlock()
	}
}
