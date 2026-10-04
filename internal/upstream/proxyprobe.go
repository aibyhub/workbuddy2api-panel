// proxyprobe.go 代理出口探测（面板「测速」用）：与真实请求完全相同的客户端构造，
// 避免「测试通过、真实请求挂」的不一致（trae2api-web v1.2.8 的教训）。
package upstream

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// ProxyProbeResult 单次代理探测结果。
type ProxyProbeResult struct {
	OK        bool   // 出口可用：经代理拿到公网回显（ipify 2xx 且带回 IP）
	ExitIP    string // 真实出口 IP（best-effort，失败为空）
	Status    int    // 上游 CN 站点的 HTTP 状态码；0 = 未拿到响应
	LatencyMs int64  // 上游请求耗时
	Err       error  // 上游传输错误（nil = 拿到响应）
}

// ProbeProxy 两步探测：① 经代理解析真实出口 IP（api.ipify.org，2xx 且带回 IP 即证明
// 出口可用）；② 经代理访问 CN 上游站点根路径，记录状态码与延迟（根路径 4xx 属正常
// 回包，不拿 <400 判失败）。关键：必须走 clientFor（而不是另建 Transport）——否则
// 「测试通过、真实请求挂」会重演。
func (c *Client) ProbeProxy(proxyURL string, timeout time.Duration) ProxyProbeResult {
	var res ProxyProbeResult
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	std, _ := c.clientFor(&auth.Auth{ProxyURL: proxyURL})
	hc := &http.Client{Timeout: timeout, Transport: std.Transport}

	// ① 出口 IP / 出口可用性
	if r1, err := hc.Get("https://api.ipify.org"); err == nil {
		raw, _ := io.ReadAll(io.LimitReader(r1.Body, 64))
		r1.Body.Close()
		res.ExitIP = strings.TrimSpace(string(raw))
		res.OK = r1.StatusCode >= 200 && r1.StatusCode < 300 && res.ExitIP != ""
	}

	// ② 上游可达性（状态码 + 延迟；4xx 属正常，不做 ok 判定）
	start := time.Now()
	resp, err := hc.Get(c.ChatBaseCN + "/")
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Err = err
		return res
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	res.Status = resp.StatusCode
	return res
}
