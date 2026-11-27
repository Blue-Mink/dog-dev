package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ghpp/ghpp/internal/config"
)

// 用真实的 fnpack.json 结构（releases -> packages）验证版本比较与架构选择。
func TestNewestKSpeederURL_FromReleases(t *testing.T) {
	arch := "x86"
	if runtime.GOARCH == "arm64" {
		arch = "arm"
	}
	other := "arm"
	if arch == "arm" {
		other = "x86"
	}
	app := manifestApp{
		Releases: map[string]manifestRelease{
			"0.8.2": {Packages: map[string]manifestPackage{
				arch: {DownloadURL: "https://example.com/kspeeder-0.8.2-old.fpk"},
			}},
			"0.8.9": {Packages: map[string]manifestPackage{
				arch:  {DownloadURL: "https://example.com/kspeeder-0.8.9-new.fpk"},
				other: {DownloadURL: "https://example.com/kspeeder-0.8.9-other.fpk"},
			}},
			// 版本号更高的条目只有别的架构，应被跳过
			"0.9.0": {Packages: map[string]manifestPackage{
				other: {DownloadURL: "https://example.com/kspeeder-0.9.0-other.fpk"},
			}},
		},
	}
	got, ver, err := newestKSpeederURL(app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "https://example.com/kspeeder-0.8.9-new.fpk" || ver != "0.8.9" {
		t.Fatalf("got url=%q ver=%q, want 0.8.9 new", got, ver)
	}
}

// 顶层平铺 download_url / version（moo catalog 某些版本）也能解析。
func TestNewestKSpeederURL_Flat(t *testing.T) {
	app := manifestApp{
		DownloadURL: "https://example.com/kspeeder-flat.fpk",
		Version:     "1.2.3",
	}
	got, ver, err := newestKSpeederURL(app)
	if err != nil || got != app.DownloadURL || ver != "1.2.3" {
		t.Fatalf("flat: got %q %q err=%v", got, ver, err)
	}
}

// findKSpeederApp 同时支持 fnpack.json({apps:...}) 与 moo catalog({sources:{..:{apps:..}}})。
func TestFindKSpeederApp_BothShapes(t *testing.T) {
	fnpack := []byte(`{"apps":{"kspeeder":{"download_url":"https://x/fnpack.fpk","version":"0.8.9"}}}`)
	if a, ok := findKSpeederApp(fnpack); !ok || a.DownloadURL != "https://x/fnpack.fpk" {
		t.Fatalf("fnpack shape failed: ok=%v a=%+v", ok, a)
	}
	catalog := []byte(`{"sources":{"Blue-Mink":{"apps":{"kspeeder":{"download_url":"https://x/moo.fpk","version":"0.8.9"}}}}}`)
	if a, ok := findKSpeederApp(catalog); !ok || a.DownloadURL != "https://x/moo.fpk" {
		t.Fatalf("catalog shape failed: ok=%v a=%+v", ok, a)
	}
}

func TestVersionGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.8.9", "0.8.2", true},
		{"0.8.2", "0.8.9", false},
		{"2.4.15", "2.4.9", true},
		{"1.0.0", "0.9.9", true},
		{"0.8.9", "0.8.9", false},
	}
	for _, c := range cases {
		if got := versionGreater(c.a, c.b); got != c.want {
			t.Errorf("versionGreater(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

// 管道清单：arch|version|filename|url|sha256|size。验证注释跳过、架构过滤、
// 取最高版本、sha256/size 解析。sha 用 64 个 Hex 字符，size 用十进制。
func TestParseKSpeederList(t *testing.T) {
	shaA := "1111111111111111111111111111111111111111111111111111111111111111"
	shaB := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	raw := []byte(`# dog-dev kspeeder 下载清单

x86|0.8.2|kspeeder-0.8.2-x86.fpk|https://example.com/0.8.2.fpk|` + shaA + `|12345
x86|0.8.9|kspeeder-0.8.9-x86.fpk|https://example.com/0.8.9.fpk|` + shaB + `|67890
arm|0.9.0|kspeeder-0.9.0-arm.fpk|https://example.com/0.9.0-arm.fpk|` + shaA + `|9999
x86|0.9.1|bad-sha|https://example.com/bad.fpk|nothex|100
x86|0.9.2|bad-url|not-a-url|` + shaA + `|100
`)
	got, err := parseKSpeederList(raw, "x86")
	if err != nil {
		t.Fatalf("parseKSpeederList: %v", err)
	}
	if got.Version != "0.8.9" || got.DownloadURL != "https://example.com/0.8.9.fpk" ||
		got.SHA256 != shaB || got.Size != 67890 {
		t.Fatalf("got %+v, want 0.8.9 with shaB/67890", got)
	}
	arm, err := parseKSpeederList(raw, "arm")
	if err != nil || arm.Version != "0.9.0" || arm.Size != 9999 {
		t.Fatalf("arm got %+v err=%v, want 0.9.0", arm, err)
	}
}

// 清单全是无有效条目/无目标架构时必须报错，才能触发 JSON 回退链。
func TestParseKSpeederList_NoValidEntry(t *testing.T) {
	sha := "1111111111111111111111111111111111111111111111111111111111111111"
	raw := []byte("# comment\narm|0.8.9|a|https://x/b.fpk|" + sha + "|123\n")
	if _, err := parseKSpeederList(raw, "x86"); err == nil {
		t.Fatal("want error when no x86 entry")
	}
	if _, err := parseKSpeederList([]byte(""), "x86"); err == nil {
		t.Fatal("want error on empty list")
	}
}

// 端到端验证「接通」：把清单地址指向本地 httptest，解析器必须读到管道清单，
// 返回 URL/版本/sha256/size/source；改清单内容后（TTL 过期）解析结果跟着变。
func TestResolveKSpeederDownload_FromList(t *testing.T) {
	sha := "2222222222222222222222222222222222222222222222222222222222222222"
	list := "x86|0.8.9|kspeeder-0.8.9-x86.fpk|https://example.com/0.8.9.fpk|" + sha + "|67890\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, list)
	}))
	defer srv.Close()

	orig := kspeederListURLs
	kspeederListURLs = []string{srv.URL + "/lists/kspeeder.txt"}
	defer func() { kspeederListURLs = orig }()
	kspeederDownloadCache.mu.Lock()
	kspeederDownloadCache.url, kspeederDownloadCache.version = "", ""
	kspeederDownloadCache.sha256, kspeederDownloadCache.size = "", 0
	kspeederDownloadCache.source, kspeederDownloadCache.at = "", time.Time{}
	kspeederDownloadCache.mu.Unlock()

	cfg := &config.Config{}
	url, ver, gotSHA, gotSize, src := resolveKSpeederDownload(cfg, nil)
	if url != "https://example.com/0.8.9.fpk" || ver != "0.8.9" || gotSHA != sha || gotSize != 67890 {
		t.Fatalf("got url=%q ver=%q sha=%q size=%d src=%q", url, ver, gotSHA, gotSize, src)
	}
	if !strings.HasPrefix(src, "list:") {
		t.Fatalf("source should be list, got %q", src)
	}

	// 改清单 + 让缓存过期：解析器必须读到新内容（改清单不重建 FPK 的核心语义）。
	newSha := "3333333333333333333333333333333333333333333333333333333333333333"
	list = "x86|0.9.0|kspeeder-0.9.0-x86.fpk|https://example.com/0.9.0.fpk|" + newSha + "|11111\n"
	kspeederDownloadCache.mu.Lock()
	kspeederDownloadCache.at = time.Now().Add(-kspeederDownloadTTL - time.Minute)
	kspeederDownloadCache.mu.Unlock()
	url, ver, gotSHA, gotSize, _ = resolveKSpeederDownload(cfg, nil)
	if url != "https://example.com/0.9.0.fpk" || ver != "0.9.0" || gotSHA != newSha || gotSize != 11111 {
		t.Fatalf("after list change: got url=%q ver=%q sha=%q size=%d", url, ver, gotSHA, gotSize)
	}
}
