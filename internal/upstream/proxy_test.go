// proxy_test.go clientFor 选路测试：auth proxy_url > 全局兜底 > 直连 的优先级、
// 按代理 URL 缓存、非法配置兜底直连。不发起网络请求，只断言客户端选择。
package upstream

import (
	"net/http"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func newProxyTestClient() *Client {
	tr := &http.Transport{}
	return &Client{
		HTTP:          &http.Client{Timeout: 120 * 1e9, Transport: tr},
		ChatHTTP:      &http.Client{Transport: tr},
		ChatBaseCN:    "https://copilot.tencent.com",
		GlobalEnabled: true,
	}
}

func TestClientForDirectWhenNoProxy(t *testing.T) {
	c := newProxyTestClient()
	a := &auth.Auth{UID: "u1"}
	std, stream := c.clientFor(a)
	if std != c.HTTP || stream != c.ChatHTTP {
		t.Fatal("no proxy: must return default client pair")
	}
	// 全局兜底未设置时同理。
	c.SetGlobalProxyURL("")
	if std, _ := c.clientFor(nil); std != c.HTTP {
		t.Fatal("nil auth without global: must be direct")
	}
}

func TestClientForAccountProxyWinsAndCaches(t *testing.T) {
	c := newProxyTestClient()
	c.SetGlobalProxyURL("http://10.0.0.9:9999")
	a := &auth.Auth{UID: "u1", ProxyURL: "socks5://10.0.0.1:10529"}
	std1, stream1 := c.clientFor(a)
	if std1 == c.HTTP || stream1 == c.ChatHTTP {
		t.Fatal("account proxy: must not be default pair")
	}
	if std1.Timeout != c.HTTP.Timeout {
		t.Fatalf("proxy std client must inherit std timeout, got %v", std1.Timeout)
	}
	if stream1.Timeout != 0 {
		t.Fatalf("proxy stream client must have no total timeout, got %v", stream1.Timeout)
	}
	// 同代理第二次调用：缓存命中，同一指针。
	std2, stream2 := c.clientFor(a)
	if std2 != std1 || stream2 != stream1 {
		t.Fatal("proxy clients must be cached per URL")
	}
	// 账号代理优先于全局兜底：不同代理 URL 得到不同客户端。
	gStd, _ := c.clientFor(&auth.Auth{UID: "u2"})
	if gStd == std1 {
		t.Fatal("global fallback must be a different client than account proxy")
	}
	if tr, ok := std1.Transport.(*http.Transport); !ok || tr.Proxy == nil {
		t.Fatal("proxy transport must carry Proxy func")
	}
}

func TestClientForInvalidProxyFallsBackDirect(t *testing.T) {
	c := newProxyTestClient()
	a := &auth.Auth{UID: "u1", ProxyURL: "ftp://not-a-proxy:21"}
	std, stream := c.clientFor(a)
	if std != c.HTTP || stream != c.ChatHTTP {
		t.Fatal("invalid proxy: must fall back to default (direct) pair")
	}
	// 非法结果同样缓存（同 URL 二次调用不刷日志、不重建）。
	std2, _ := c.clientFor(a)
	if std2 != std {
		t.Fatal("invalid fallback must be cached")
	}
}

func TestValidProxyScheme(t *testing.T) {
	for _, s := range []string{"http", "https", "socks5", "socks5h"} {
		if !ValidProxyScheme(s) {
			t.Fatalf("scheme %q should be valid", s)
		}
	}
	for _, s := range []string{"ftp", "socks4", "", "SOCKS5"} {
		if ValidProxyScheme(s) {
			t.Fatalf("scheme %q should be invalid", s)
		}
	}
}
