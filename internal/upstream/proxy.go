// proxy.go 按账号独立出口代理：clientFor 选路 + 代理客户端缓存。
// 与 trae2api-web 同款模式（线上 SOCKS5 出口实测验证）：优先级
// auth proxy_url > config upstream.proxy_url（全局兜底）> 直连；
// 按代理 URL 缓存 {std, stream} 客户端对，非法配置兜底直连并缓存（避免刷日志）。
package upstream

import (
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// proxyPair 一个代理出口对应的客户端对；std 有总超时（短 RPC），stream 无（SSE 聊天）。
type proxyPair struct{ std, stream *http.Client }

// clientFor 返回该账号可用的 HTTP 客户端（按账号独立代理出口）。
// 默认路径（无代理）与既有行为完全一致：返回默认客户端对，测试注入的 mock 不受影响。
func (c *Client) clientFor(a *auth.Auth) (std, stream *http.Client) {
	std, stream = c.HTTP, c.chatHTTP()
	proxy := ""
	if a != nil {
		proxy = strings.TrimSpace(a.ProxyURLValue())
	}
	if proxy == "" {
		proxy = c.GlobalProxyURL()
	}
	if proxy == "" {
		return std, stream
	}
	if v, ok := c.proxyClients.Load(proxy); ok {
		p := v.(*proxyPair)
		return p.std, p.stream
	}
	u, err := url.Parse(proxy)
	if err != nil || !ValidProxyScheme(u.Scheme) {
		// 回退直连 = 真实出口 IP 不再是该账号配置的代理，务必留证。
		// 校验层（panel validProxyURL）与本处共用 ValidProxyScheme，正常不该走到这里。
		log.Printf("proxy config invalid (%q) — fallback to DIRECT", proxy)
		p := &proxyPair{std: std, stream: stream}
		c.proxyClients.Store(proxy, p)
		return p.std, p.stream
	}
	var tr *http.Transport
	if base, ok := std.Transport.(*http.Transport); ok && base != nil {
		tr = base.Clone() // 复制连接层加固参数（Clone 不复制既有连接），再覆盖代理
	} else {
		tr = &http.Transport{}
	}
	tr.Proxy = http.ProxyURL(u)
	// newTransport 已置空 TLSNextProto（真正禁 h2）；Clone 原样保留该设置，代理出口
	// 与直连同为 HTTP/1.1，无 trae 侧 ALPN/h2 半协商问题。
	p := &proxyPair{
		std:    &http.Client{Timeout: std.Timeout, Transport: tr},
		stream: &http.Client{Transport: tr}, // 无总超时（SSE）
	}
	c.proxyClients.Store(proxy, p)
	return p.std, p.stream
}

// ValidProxyScheme 报告代理 URL 的 scheme 是否受支持。
// 单一事实来源：面板校验（validProxyURL）与传输层（clientFor）共用同一套规则，
// 避免「保存成功、实际静默直连」。socks5h 与 socks5 在 Go 内部等价（都走 socks5
// 拨号、域名交给代理解析），一并接受。
func ValidProxyScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "socks5", "socks5h":
		return true
	}
	return false
}

// ValidProxyURL 报告代理地址整体是否可用（可解析 + host 非空 + scheme 受支持）。
func ValidProxyURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Host != "" && ValidProxyScheme(u.Scheme)
}

// SetGlobalProxyURL 设置全局兜底代理（config upstream.proxy_url，面板热改即时生效）。
func (c *Client) SetGlobalProxyURL(raw string) { c.globalProxy.Store(strings.TrimSpace(raw)) }

// GlobalProxyURL 返回当前全局兜底代理（空 = 未设置，全部直连或按账号 proxy_url）。
func (c *Client) GlobalProxyURL() string {
	if v, ok := c.globalProxy.Load().(string); ok {
		return v
	}
	return ""
}
