package api

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ghpp/ghpp/internal/config"
)

// kspeederDownloadCache 缓存从远端清单解析出的最新下载地址，避免每次
// 打开控制台都打一次 GitHub。TTL 内命中；TTL 过期后惰性刷新；刷新失败
// 时沿用上一份成功结果（stale-while-error），彻底失败才回退内置地址。
var kspeederDownloadCache = struct {
	mu      sync.Mutex
	url     string
	version string
	sha256  string
	size    int64
	source  string
	at      time.Time
}{}

const kspeederDownloadTTL = 30 * time.Minute

// kspeederListURLs 是远端「管道分隔清单」的候选地址。与 Moo 同一份清单语义：
// 每行 arch|version|filename|url|sha256|size，改清单不重建 FPK，TTL 后自动生效。
// 第一个是 dog-dev 自己的清单（发布后即权威源），第二个是 Moo 的同一份清单，
// 后两个是 jsdelivr 镜像兜底。全部失败才回退老的 JSON catalog 链。
var kspeederListURLs = []string{
	"https://raw.githubusercontent.com/Blue-Mink/dog-dev/main/lists/kspeeder.txt",
	"https://raw.githubusercontent.com/Blue-Mink/moo/main/lists/kspeeder.txt",
	"https://fastly.jsdelivr.net/gh/Blue-Mink/dog-dev@main/lists/kspeeder.txt",
	"https://fastly.jsdelivr.net/gh/Blue-Mink/moo@main/lists/kspeeder.txt",
}

// kspeederListEntry 是清单中一行的结构化结果（sha256/size 供下载方双重校验）。
type kspeederListEntry struct {
	Arch        string
	Version     string
	Filename    string
	DownloadURL string
	SHA256      string
	Size        int64
}

// 远端清单候选地址。第一个是权威源（FnDepot 官方索引），后面是镜像兜底，
// 与 moo 读取同一份清单（moo 的 Blue-Mink source index_url 即第一个）。
var kspeederManifestURLs = []string{
	"https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/fnpack.json",
	"https://fastly.jsdelivr.net/gh/Blue-Mink/FnDepot@main/fnpack.json",
}

// moo 客户端缓存的目录清单：直接复用本机 moo 已经拉好的数据，避免重复
// 出网（用户可选「直接读 moo 的清单」这条更快更稳的路径）。
var kspeederMooCatalogPaths = []string{
	"/vol1/@appdata/moo/cache/catalog.json",
}

// manifestApp 对应 fnpack.json / moo catalog 里某个 app 的条目。
type manifestApp struct {
	DownloadURL string                     `json:"download_url"`
	Version     string                     `json:"version"`
	Releases    map[string]manifestRelease `json:"releases"`
}

type manifestRelease struct {
	Packages map[string]manifestPackage `json:"packages"`
}

type manifestPackage struct {
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

// resolveKSpeederDownloadURL 是 resolveKSpeederDownload 的兼容包装（仅 URL + 版本）。
func resolveKSpeederDownloadURL(cfg *config.Config, logf func(level, format string, args ...any)) (string, string) {
	url, ver, _, _, _ := resolveKSpeederDownload(cfg, logf)
	return url, ver
}

// resolveKSpeederDownload 决定控制台展示的 kspeeder 下载地址及其校验信息。
// 优先级：用户自定义 > 缓存 > 远端管道清单(lists/kspeeder.txt) > moo 本地
// catalog > 远端 fnpack.json > 内置默认。管道清单是权威源：改清单不重建 FPK。
func resolveKSpeederDownload(cfg *config.Config, logf func(level, format string, args ...any)) (string, string, string, int64, string) {
	custom := strings.TrimSpace(cfg.KSpeeder.DownloadURL)
	if custom != "" && !config.IsDefaultKSpeederDownloadURL(custom) {
		return custom, "custom", "", 0, "custom"
	}

	// TTL 内直接用缓存（含 sha256/size，保证与清单显示一致）。
	kspeederDownloadCache.mu.Lock()
	cachedURL, cachedVer, cachedSHA, cachedSize, cachedSrc, cachedAt :=
		kspeederDownloadCache.url, kspeederDownloadCache.version,
		kspeederDownloadCache.sha256, kspeederDownloadCache.size,
		kspeederDownloadCache.source, kspeederDownloadCache.at
	kspeederDownloadCache.mu.Unlock()
	if cachedURL != "" && time.Since(cachedAt) < kspeederDownloadTTL {
		return cachedURL, cachedVer, cachedSHA, cachedSize, cachedSrc
	}

	// ① 权威源：远端管道清单。
	url, ver, sha256, size, source, err := kspeederURLFromList()
	if err != nil {
		if logf != nil {
			logf("DEBUG", "kspeeder 下载清单：远端 lists/kspeeder.txt 不可用：%v", err)
		}
		// ② 回退老链：moo 本地 catalog（零出网）→ 远端 fnpack.json。
		var u2, v2 string
		u2, v2, err = kspeederURLFromMooCatalog()
		if err != nil {
			if logf != nil {
				logf("DEBUG", "kspeeder 下载清单：读 moo catalog 失败：%v", err)
			}
			u2, v2, err = kspeederURLFromManifest()
		}
		if err != nil {
			if logf != nil {
				logf("WARN", "kspeeder 下载清单：解析远端清单失败（%v），回退内置地址", err)
			}
			// 刷新失败但仍有旧值：继续用旧值，不回退到过时的内置版本。
			if cachedURL != "" {
				return cachedURL, cachedVer, cachedSHA, cachedSize, cachedSrc
			}
			if custom != "" {
				return custom, "default", "", 0, "default"
			}
			return config.KSpeederDefaultDownloadURL(), "default", "", 0, "default"
		}
		url, ver, sha256, size, source = u2, v2, "", 0, "manifest"
	}

	kspeederDownloadCache.mu.Lock()
	kspeederDownloadCache.url, kspeederDownloadCache.version = url, ver
	kspeederDownloadCache.sha256, kspeederDownloadCache.size = sha256, size
	kspeederDownloadCache.source, kspeederDownloadCache.at = source, time.Now()
	kspeederDownloadCache.mu.Unlock()
	return url, ver, sha256, size, source
}

// kspeederURLFromList 拉取并解析远端管道清单，返回本机架构最新版的完整条目。
func kspeederURLFromList() (string, string, string, int64, string, error) {
	arch := "x86"
	if strings.Contains(runtime.GOARCH, "arm") {
		arch = "arm"
	}
	client := &http.Client{Timeout: 12 * time.Second}
	var lastErr error
	for _, src := range kspeederListURLs {
		req, _ := http.NewRequest(http.MethodGet, src, nil)
		req.Header.Set("User-Agent", "dog-dev/kspeeder-list")
		resp, e := client.Do(req)
		if e != nil {
			lastErr = e
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: HTTP %d", src, resp.StatusCode)
			continue
		}
		buf, re := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if re != nil && len(buf) == 0 {
			lastErr = re
			continue
		}
		entry, pe := parseKSpeederList(buf, arch)
		if pe != nil {
			lastErr = fmt.Errorf("%s: %v", src, pe)
			continue
		}
		return entry.DownloadURL, entry.Version, entry.SHA256, entry.Size, "list:" + src, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无清单地址")
	}
	return "", "", "", 0, "", lastErr
}

// parseKSpeederList 解析管道清单：arch|version|filename|url|sha256|size。
// # 开头与空行忽略；只取目标架构、版本号最大的一行；sha256 必须是 64 位
// 十六进制、size 必须是正整数（这样清单里的校验字段才真正可用）。
func parseKSpeederList(raw []byte, arch string) (kspeederListEntry, error) {
	var best kspeederListEntry
	found := false
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 6 {
			continue
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if parts[0] != arch || parts[1] == "" {
			continue
		}
		u, e := url.Parse(parts[3])
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		if len(parts[4]) != 64 {
			continue
		}
		if _, e := hex.DecodeString(parts[4]); e != nil {
			continue
		}
		size, e := strconv.ParseInt(parts[5], 10, 64)
		if e != nil || size <= 0 {
			continue
		}
		entry := kspeederListEntry{
			Arch: parts[0], Version: parts[1], Filename: parts[2],
			DownloadURL: parts[3], SHA256: strings.ToLower(parts[4]), Size: size,
		}
		if !found || versionGreater(entry.Version, best.Version) {
			best, found = entry, true
		}
	}
	if err := sc.Err(); err != nil {
		return kspeederListEntry{}, err
	}
	if !found {
		return kspeederListEntry{}, fmt.Errorf("清单无 %s 架构有效条目", arch)
	}
	return best, nil
}

// kspeederURLFromMooCatalog 直接从 moo 已缓存的 catalog.json 解析 kspeeder。
func kspeederURLFromMooCatalog() (string, string, error) {
	var lastErr error
	for _, p := range kspeederMooCatalogPaths {
		raw, err := os.ReadFile(p)
		if err != nil {
			lastErr = err
			continue
		}
		app, ok := findKSpeederApp(raw)
		if !ok {
			lastErr = fmt.Errorf("%s: 未找到 kspeeder 条目", p)
			continue
		}
		if u, v, e := newestKSpeederURL(app); e == nil {
			return u, v, nil
		} else {
			lastErr = e
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无 moo catalog 路径")
	}
	return "", "", lastErr
}

// kspeederURLFromManifest 拉取远端 fnpack.json（与 moo 同源），解析最新版。
func kspeederURLFromManifest() (string, string, error) {
	client := &http.Client{Timeout: 12 * time.Second}
	var lastErr error
	for _, m := range kspeederManifestURLs {
		req, _ := http.NewRequest(http.MethodGet, m, nil)
		req.Header.Set("User-Agent", "dog-dev/kspeeder-manifest")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: HTTP %d", m, resp.StatusCode)
			continue
		}
		// 限制读取 4MB，防止异常大文件。
		buf := make([]byte, 0, 256<<10)
		tmp := make([]byte, 32<<10)
		for len(buf) < 4<<20 {
			n, e := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if e != nil {
				break
			}
		}
		resp.Body.Close()
		app, ok := findKSpeederApp(buf)
		if !ok {
			lastErr = fmt.Errorf("%s: 未找到 kspeeder 条目", m)
			continue
		}
		if u, v, e := newestKSpeederURL(app); e == nil {
			return u, v, nil
		} else {
			lastErr = e
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无清单地址")
	}
	return "", "", lastErr
}

// findKSpeederApp 从清单 JSON（fnpack.json 或 moo catalog）里找 kspeeder 条目。
// moo catalog 结构为 {sources:{<name>:{apps:{kspeeder:{...}}}}}；
// fnpack.json 结构为 {apps:{kspeeder:{...}}}。
func findKSpeederApp(raw []byte) (manifestApp, bool) {
	var top struct {
		Apps    map[string]manifestApp `json:"apps"`
		Sources map[string]struct {
			Apps map[string]manifestApp `json:"apps"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return manifestApp{}, false
	}
	if a, ok := top.Apps["kspeeder"]; ok {
		return a, true
	}
	// moo catalog：遍历所有 source，任一含 kspeeder 即可。
	names := make([]string, 0, len(top.Sources))
	for n := range top.Sources {
		names = append(names, n)
	}
	sort.Strings(names) // 确定性：优先 Blue-Mink 之外的顺序无关，取第一个命中即可
	for _, n := range names {
		if a, ok := top.Sources[n].Apps["kspeeder"]; ok {
			return a, true
		}
	}
	return manifestApp{}, false
}

// newestKSpeederURL 从 app 条目取最新版本的下载地址，按本机架构选包。
func newestKSpeederURL(app manifestApp) (string, string, error) {
	arch := "x86"
	if strings.Contains(runtime.GOARCH, "arm") {
		arch = "arm"
	}
	// releases：版本 -> packages。按版本号倒序取第一个有目标架构包的版本。
	if len(app.Releases) > 0 {
		vers := make([]string, 0, len(app.Releases))
		for v := range app.Releases {
			vers = append(vers, v)
		}
		sort.Slice(vers, func(i, j int) bool { return versionGreater(vers[i], vers[j]) })
		for _, v := range vers {
			if u, ok := app.Releases[v].Packages[arch]; ok && strings.TrimSpace(u.DownloadURL) != "" {
				return strings.TrimSpace(u.DownloadURL), v, nil
			}
		}
	}
	// 顶层的 version/download_url（moo catalog 某些版本平铺在此）。
	if u := strings.TrimSpace(app.DownloadURL); u != "" {
		return u, app.Version, nil
	}
	return "", "", fmt.Errorf("kspeeder 清单无 %s 架构下载地址", arch)
}

// versionGreater 比较形如 0.8.9 / 2.4.15 的点分数字版本，r 更大返回 true。
func versionGreater(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var na, nb int
		if i < len(pa) {
			na, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			nb, _ = strconv.Atoi(pb[i])
		}
		if na != nb {
			return na > nb
		}
	}
	return false
}
