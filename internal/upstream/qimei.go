// qimei.go 腾讯 qimei 设备指纹解析（对照官方客户端 qimei.dll 体系，2026-10-25 逆向）。
//
// 官方客户端：qimei.dll（闭源 FFI，appKey 0WIN070JLSH64IEE）生成 36 位 hex 的
// qimei36，缓存在 ~/.workbuddy/qimei-cache.json，由 QimeiEventProcessor 给每条
// /v2/report 事件补 qimei36 字段。qimei 是**设备级**标识（一台机器一个值，账号无关），
// 与 X-Device-Token（上游签发的设备令牌）、machineId/sessionId（网关按 uid 派生的
// 虚拟机指纹）是三个不同层。
//
// 网关解析优先级：
//  1. Client.Qimei36（config upstream.qimei36）—— 显式值
//  2. Client.QimeiFile（config upstream.qimei_file）—— 文件兜底（同机部署可直接指向
//     客户端 qimei-cache.json 原文；也接受纯 36 hex 文本）；5 分钟读取缓存
//  3. 内置派生常量 —— 不依赖外部输入：sha256("wb2a:qimei36") 截 36 hex，
//     每**实例**恒定一台「虚拟设备」（qimei 语义是设备级，与账号无关，故不按 uid 派生）
package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// 注：resolveQimei36 不接收 auth.Auth——qimei 语义是设备级标识（一台机器一个值），
// 与账号无关；machineId/sessionId 的按 uid 派生是另一层（虚拟机指纹）。

const (
	// qimeiFileTTL 文件兜底的读取缓存窗口（与 device_token.go 同口径）。
	qimeiFileTTL = 5 * time.Minute
	// qimeiFileMaxLen 文件上限：允许整个 qimei-cache.json 原样落盘（远小于真实缓存文件）。
	qimeiFileMaxLen = 4096
)

// qimeiFileCacheEntry 文件读取缓存条目。
type qimeiFileCacheEntry struct {
	value  string
	readAt time.Time
}

var (
	qimeiMu    sync.Mutex
	qimeiCache = map[string]qimeiFileCacheEntry{}
)

// resolveQimei36 生效的 qimei36（config 值 > 文件兜底 > 内置派生常量）。
func (c *Client) resolveQimei36() string {
	if c != nil {
		if c.Qimei36 != "" {
			return c.Qimei36
		}
		if c.QimeiFile != "" {
			if v := readQimei36File(c.QimeiFile); v != "" {
				return v
			}
		}
	}
	return deriveInstanceQimei36()
}

// deriveInstanceQimei36 内置派生：固定盐（无 uid）——qimei 语义是设备级，
// 同实例所有账号共享同一台「虚拟设备」。
func deriveInstanceQimei36() string {
	sum := sha256.Sum256([]byte("wb2a:qimei36"))
	return hex.EncodeToString(sum[:18]) // 36 hex chars
}

// readQimei36File 读文件兜底：接受两种形态——纯 36 hex 文本，或客户端
// qimei-cache.json 原文（解析 $.qimei36）。读失败/超限/解析失败返回空串
// （调用方回落派生值）；带 TTL 缓存，读失败同样缓存防每请求重读坏文件。
func readQimei36File(path string) string {
	qimeiMu.Lock()
	defer qimeiMu.Unlock()
	if e, ok := qimeiCache[path]; ok && time.Since(e.readAt) < qimeiFileTTL {
		return e.value
	}
	raw, err := os.ReadFile(path)
	v := ""
	if err == nil {
		v = parseQimei36(raw)
	} else {
		v = ""
	}
	qimeiCache[path] = qimeiFileCacheEntry{value: v, readAt: time.Now()}
	return v
}

// parseQimei36 从文件内容提取 qimei36：JSON 形态取 $.qimei36；纯文本形态 trim
// 引号与首尾空白后取值。不校验 36 hex 严格形态（上游对字段值的校验未知，宽松透传）。
func parseQimei36(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "{") {
		var obj struct {
			Qimei36 string `json:"qimei36"`
		}
		if err := json.Unmarshal([]byte(s), &obj); err == nil && strings.TrimSpace(obj.Qimei36) != "" {
			return strings.TrimSpace(obj.Qimei36)
		}
		return ""
	}
	s = strings.Trim(s, "\"' \t\r\n")
	if s == "" || len(s) > 128 {
		return ""
	}
	return s
}
