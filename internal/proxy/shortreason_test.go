package proxy

import (
	"errors"
	"strings"
	"testing"
)

func TestShortReasonNeverLeaksURL(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New(`Get "https://github.com/secret/path?token=abc": context deadline exceeded`), "请求超时"},
		{errors.New(`Get "https://api.github.com/x": EOF`), "响应中断"},
		{errors.New(`dial tcp: lookup foo on 8.8.8.8:53: no such host`), "域名解析失败"},
		{errors.New(`Get "https://x/y": dial tcp 1.2.3.4:443: connect: connection refused`), "连接被拒"},
		{errors.New(`tls: failed to verify certificate: x509: invalid`), "证书或握手失败"},
		{errors.New(`Get "https://x": unexpected status code 502`), "HTTP 502"},
		{errors.New(`something totally new`), "连接失败"},
	}
	for _, c := range cases {
		got := shortReason(c.err)
		if got != c.want {
			t.Errorf("shortReason(%q) = %q, want %q", c.err, got, c.want)
		}
		// 核心不变量：结果里绝不能出现 URL / 引号 / 斜杠 / query。
		for _, bad := range []string{"http", "https", "://", "/", "?", "token", "Get", "\""} {
			if strings.Contains(got, bad) {
				t.Errorf("shortReason(%q) leaked %q: %q", c.err, bad, got)
			}
		}
	}
}

func TestShortReasonNil(t *testing.T) {
	if got := shortReason(nil); got != "" {
		t.Errorf("nil err want empty, got %q", got)
	}
}
