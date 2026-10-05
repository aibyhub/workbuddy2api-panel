// transport.go 出站 Transport 构造的单一事实来源（连接层加固，吸收 kongjianguan
// 4 连击实测经验的前三件，见 .claude/reports/fork-scan-absorb.md T-2）：
// 真正禁 h2 / TLS 握手超时 / 短 keepalive 探测，全参数集中定义可测试可调整。
// 第四件 DisableKeepAlives 按报告 trade-off 不吸收（每请求 TLS 握手开销与连接
// 复用意图相反），分析见 .claude/reports/transport-hardening.md。
package upstream

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/tlsfp"
)

// 连接层参数集中定义（与 server/backoff.go 同风格：一处定义，测试可回读断言）。
const (
	// dialTimeout TCP 连接建立上限。半死连接的第一道闸：连不上就快速失败
	// 轮转换号，不再干等系统 TCP 重传窗口（kongjianguan 实测半开 TCP 单次
	// TTFB 卡 936s——默认 Dialer 无超时上限）。
	dialTimeout = 10 * time.Second
	// dialKeepAlive TCP keepalive 探测周期。默认 Dialer 2h 才发首个探测——
	// NAT 黑洞里 2h 足够连接半死且被复用。15s 周期让死连接在 15~30s 内被
	// 内核掐掉（RST/ETIMEDOUT），复用侧立即感知而非卡到重传窗口。
	dialKeepAlive = 15 * time.Second
	// tlsHandshakeTimeout TLS 握手上限。现役此前完全缺失——握手挂起时无任何
	// 层兜底（ResponseHeaderTimeout 只在请求写完后才计时），只能干等到
	// HTTP.Client.Timeout(120s)。
	tlsHandshakeTimeout = 10 * time.Second
	// idleConnTimeout 空闲连接池保留时长。从 90s 收到 30s：WAF 风暴后上游
	// NGI 常态性掐闲置连接，90s 池里的连接多半已死（kongjianguan 同款取值）；
	// 复用侧仍有 15s keepalive 兜底识别。
	idleConnTimeout = 30 * time.Second
	// responseHeaderTimeout 聊天 SSE 首字节前（响应头）硬上限。从 120s 收到
	// 60s：kongjianguan 笔记实录成功请求 TTFB 曾到 16s（慢模型冷启动），
	// 60s ≈ 3.75× 观测最坏健康首包，留足慢冷启动余量；同时把半死连接场景的
	// 单请求卡死从 2 分钟压到 1 分钟（MaxRotate 默认 3 次的最坏轮转从 6 分钟
	// 压到 3 分钟）。不取 kongjianguan 的 20s：其 20s 是 DisableKeepAlives+
	// timedConn 20s 写超时组合的取值，我们保留连接复用，须按自身慢冷启动
	// 观测留余量。语义核对（任务书设计纪律）：ResponseHeaderTimeout 只计响应
	// 头到达前的时长，头到达后 SSE 长流不受影响（流中空闲由 IdleTimeout 监控，
	// 见 idle.go），不误杀长流——transport_test.go 有显式回归。
	//
	// 注意：本常量只是 newTransport 的构造默认，main.go 会按 config
	// upstream.header_timeout_seconds 无条件覆盖。因此生产生效值 = config
	// 解析值（未配置时 normalize 回落 timeout_seconds，默认 120），本 60s 仅作
	// 「Config 未接线/测试裸用」时的安全网——与 config.example.json 的取值
	// 对齐避免三处口径漂移（transport 60 / config 回落 120 / example 60）。
	responseHeaderTimeout = 60 * time.Second
)

// maxIdleConns / maxIdleConnsPerHost 连接池容量（既有值，一并集中定义）。
const (
	maxIdleConns        = 100
	maxIdleConnsPerHost = 20
)

// newDialer 构造出站拨号器（DialContext 的 Timeout/KeepAlive 参数集中于此，
// 供测试回读断言）。
func newDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: dialKeepAlive,
	}
}

// newTransport 构造共享出站 Transport（HTTP 与 ChatHTTP 同一实例，连接池不重复）。
// 分两层防半死连接：
//   - TLS 层：空 TLSNextProto 真正禁 h2（kongjianguan 二次修正的实证：ForceAttemptHTTP2=false
//     只对自定义 Dial 生效，默认 TLS 经 ALPN 仍协商出 h2，半死 h2 流复用表现为
//     "http2: timeout awaiting response headers"——唯一正确写法是置空映射，让 ALPN
//     完成后无 h2 协议可用，连接退回 HTTP/1.1）。
//   - TCP 层：DialContext 10s 建连上限 + 15s keepalive 探测，半开连接在建立期
//     和复用期都能被快速识别（见 dialTimeout/dialKeepAlive 注释）。
func newTransport() *http.Transport {
	dialer := newDialer()
	return &http.Transport{
		DialContext: dialer.DialContext,
		// 空 TLSNextProto（非 nil）真正禁 h2：见函数注释。必须 make 而非 nil——
		// nil 表示「让标准库注入默认 h2 映射」（kongjianguan 实测：设
		// ForceAttemptHTTP2=false 后日志仍报 h2 timeout，正是这个陷阱）。
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
}

// closeIdler 实现该接口的 RoundTripper 支持清空空闲连接池（*http.Transport、
// http2.Transport 等均满足；测试注入的自定义 RoundTripper 可选择性实现）。
type closeIdler interface {
	CloseIdleConnections()
}

// newTransportWithFingerprint 构造带 TLS 指纹复刻的 Transport（直连或按代理出口）。
// tlsfp 模式下 Proxy 必须置 nil——标准库只对非代理 https 调 DialTLSContext，代理
// 隧道由 tlsfp 拨号器内部建立，指纹握手发生在隧道出口端；UConn 非 *tls.Conn，
// TLSNextProto 置空禁 h2 的既有约定保持不变（Node 档本身无 ALPN，即 HTTP/1.1）。
// base 拨号器复用 newDialer()，保留 10s 建连 + 15s keepalive 连接层加固。
// 仅 https 代理（TLS-to-proxy 双层握手不做指纹，与 sub2api 同策略）返回错误，
// 调用方回退标准 transport 并留日志。
func newTransportWithFingerprint(proxyURL *url.URL) (*http.Transport, error) {
	dial, err := tlsfp.NewDialTLSContext(newDialer(), proxyURL)
	if err != nil {
		return nil, err
	}
	tr := newTransport()
	tr.DialTLSContext = dial
	return tr, nil
}

// TLSFingerprintEnabled 报告 TLS 指纹复刻是否已开启。
func (c *Client) TLSFingerprintEnabled() bool { return c.tlsFP.Load() }

// EnableTLSFingerprint 开启 TLS 指纹复刻：重建直连客户端对（连接池里的旧连接
// 是 Go 指纹握手，直接换新 Transport）并清空代理客户端缓存（clientFor 按需
// 重建时读取 tlsFP 走指纹路径）。仅在启动装配期调用一次（main 在超时覆盖前），
// 不做运行期热切换——热切换会让在途连接与新连接指纹不一致，反而可疑。
// 测试注入的 mock Transport（非 *http.Transport）原样保留，不误重建。
func (c *Client) EnableTLSFingerprint() {
	if c.tlsFP.Swap(true) {
		return
	}
	if tr, ok := c.HTTP.Transport.(*http.Transport); ok {
		if ftr, err := newTransportWithFingerprint(nil); err == nil {
			// 保留 main 已应用的 config 覆盖（ResponseHeaderTimeout 等导出字段）。
			ftr.ResponseHeaderTimeout = tr.ResponseHeaderTimeout
			c.HTTP = &http.Client{Timeout: c.HTTP.Timeout, Transport: ftr}
			if c.ChatHTTP != nil {
				c.ChatHTTP = &http.Client{Timeout: c.ChatHTTP.Timeout, Transport: ftr}
			}
		} else {
			log.Printf("tlsfp: enable failed (%v) — keep standard transport", err)
			c.tlsFP.Store(false)
			return
		}
	}
	c.proxyClients = sync.Map{}
	log.Printf("tlsfingerprint: ENABLED (WorkBuddy Node form, JA3 %s)", tlsfp.WorkBuddyJA3MD5)
}

// DisableTLSFingerprint 关闭 TLS 指纹复刻并回退标准传输（重建直连对 + 清缓存）。
// 与 Enable 对称的回退开关；启动装配期使用。
func (c *Client) DisableTLSFingerprint() {
	if !c.tlsFP.Swap(false) {
		return
	}
	if tr, ok := c.HTTP.Transport.(*http.Transport); ok {
		ntr := newTransport()
		ntr.ResponseHeaderTimeout = tr.ResponseHeaderTimeout
		c.HTTP = &http.Client{Timeout: c.HTTP.Timeout, Transport: ntr}
		if c.ChatHTTP != nil {
			c.ChatHTTP = &http.Client{Timeout: c.ChatHTTP.Timeout, Transport: ntr}
		}
	}
	c.proxyClients = sync.Map{}
	log.Printf("tlsfingerprint: DISABLED — standard transport")
}

// roundTripCloseIdle 在传输层请求失败后清掉 rt 所属 Transport 的空闲连接池
// （kongjianguan 第 4 件：失败连接可能仍留在空闲池里，等 IdleConnTimeout 才
// 过期，下一个请求会继续捡到它）。
//
// 挂载点（任务书「评估挂载点：错误分类处理处」的结论）：错误分类（Classify）
// 只见业务信封——传输层失败根本没有 body 可分类（见 doJSON/ChatStreamContext
// 对 read body 失败的处理：不进 Classify、不罚号）。这类失败的正确处理正是
// 连接层的池清理，故挂在与 Do 并列的传输层出口（ChatStreamContext 的 Do 错误
// 分支），而非 applyErrorPolicy。
//
// 关闭是 best-effort：rt 为 nil 或未实现 closeIdler（如测试注入的 rtFunc）时
// 静默跳过。CloseIdleConnections 只关空闲连接，不影响在途请求；瞬时代价是
// 下个请求多一次 TCP+TLS 握手，与半死连接被复用卡 60s 的风险完全不成比例。
func roundTripCloseIdle(rt http.RoundTripper) {
	if rt == nil {
		return
	}
	if ci, ok := rt.(closeIdler); ok {
		ci.CloseIdleConnections()
	}
}
