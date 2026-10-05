package upstream

import (
	"net/http"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestTLSFingerprintWiring 开关接线回归：EnableTLSFingerprint 重建直连对（带
// DialTLSContext）、clientFor 代理出口走指纹路径（Proxy 置 nil）、DisableTLSFingerprint
// 完整回退标准传输。tlsfp 握手字节正确性由 internal/tlsfp 包自测，此处只验接线。
func TestTLSFingerprintWiring(t *testing.T) {
	c := New()

	// 1) 默认（未开启）：直连 transport 无指纹拨号器。
	if c.HTTP.Transport.(*http.Transport).DialTLSContext != nil {
		t.Fatal("plain New() should not have DialTLSContext")
	}
	if c.TLSFingerprintEnabled() {
		t.Fatal("tlsFP should default to off before Enable")
	}

	// 2) 开启：直连对重建，DialTLSContext 就位；既有 ResponseHeaderTimeout 覆盖保留。
	tr := c.HTTP.Transport.(*http.Transport)
	tr.ResponseHeaderTimeout = 42 // 模拟 main 的 config 覆盖
	c.EnableTLSFingerprint()
	if !c.TLSFingerprintEnabled() {
		t.Fatal("EnableTLSFingerprint did not set flag")
	}
	ftr, ok := c.HTTP.Transport.(*http.Transport)
	if !ok || ftr.DialTLSContext == nil {
		t.Fatal("EnableTLSFingerprint did not install DialTLSContext on HTTP transport")
	}
	if ftr.ResponseHeaderTimeout != 42 {
		t.Fatalf("config override lost: ResponseHeaderTimeout = %v", ftr.ResponseHeaderTimeout)
	}
	if c.ChatHTTP.Transport != ftr {
		t.Fatal("ChatHTTP should share the fingerprinted transport")
	}

	// 3) 代理出口：clientFor 在指纹模式下构建带拨号器的 transport（Proxy 置 nil）。
	a := &auth.Auth{UID: "u1", ProxyURL: "socks5://127.0.0.1:1080"}
	std, stream := c.clientFor(a)
	if std == nil || stream == nil {
		t.Fatal("clientFor returned nil clients")
	}
	ptr, ok := std.Transport.(*http.Transport)
	if !ok || ptr.DialTLSContext == nil {
		t.Fatal("proxy path missing fingerprint DialTLSContext")
	}
	if ptr.Proxy != nil {
		t.Fatal("Proxy must be nil in fingerprint mode (dialer owns tunneling)")
	}

	// 4) https 代理不支持指纹：回退标准代理路径（Proxy 就位、无拨号器），不报错。
	a2 := &auth.Auth{UID: "u2", ProxyURL: "https://127.0.0.1:8443"}
	std2, _ := c.clientFor(a2)
	ptr2, ok := std2.Transport.(*http.Transport)
	if !ok || ptr2.Proxy == nil || ptr2.DialTLSContext != nil {
		t.Fatal("https proxy should fall back to standard transport with Proxy set")
	}

	// 5) 关闭：完整回退（无拨号器、缓存清空后重建为标准路径）。
	c.DisableTLSFingerprint()
	if c.TLSFingerprintEnabled() {
		t.Fatal("DisableTLSFingerprint did not clear flag")
	}
	if rtr, ok := c.HTTP.Transport.(*http.Transport); !ok || rtr.DialTLSContext != nil {
		t.Fatal("DisableTLSFingerprint did not restore plain transport")
	}
	std3, _ := c.clientFor(a)
	ptr3, ok := std3.Transport.(*http.Transport)
	if !ok || ptr3.Proxy == nil || ptr3.DialTLSContext != nil {
		t.Fatal("after disable, proxy path should use standard Proxy transport")
	}
}
