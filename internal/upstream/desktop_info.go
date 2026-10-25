// desktop_info.go 桌面指纹的硬件字段文件源（对照官方客户端 OSEventProcessor 语义）。
//
// 客户端真实取值（cli/dist/codebuddy-lite-wb.mjs 实证）：
//
//	cpuCores   = os.cpus().length（逻辑处理器数）
//	memorySize = Math.round(os.totalmem()/1024/1024/1024)（GB 整数）
//	osVersion  = os.release()（如 "10.0.26200"）
//	os/arch    = "win32"/"x64"
//
// 网关默认值是历史抓包时的虚构机器（20 核/24GB/26220）。2026-10-25 设备信任
// 升级后 qimei36 与 X-Device-Token 已是**本机真值**——硬件字段若仍是虚构值，
// 同一账号的设备指纹自相矛盾（qimei 档案说 A 机、事件硬件说 B 机），上游可
// 交叉校验。本文件源允许把铸令牌那台机器的**真实 specs** 落盘注入：
// scripts/refresh_device_trust.sh 每日产出一并同步。文件缺失/非法时回落
// 内置默认（零回归），字段任一非法则整体拒绝（不部分生效，避免混合形态）。
package upstream

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// desktopInfo 桌面事件指纹的硬件字段（json 键与客户端事件字段同名）。
type desktopInfo struct {
	Os        string `json:"os"`
	Arch      string `json:"arch"`
	OsVersion string `json:"osVersion"`
	CpuCores  int    `json:"cpuCores"`
	MemoryGB  int    `json:"memorySize"`
}

// desktopInfoFallback 内置默认（历史抓包机器形态）：文件源缺失/非法时兜底，
// 保持与旧版出站形态完全一致。
var desktopInfoFallback = desktopInfo{
	Os:        "win32",
	Arch:      "x64",
	OsVersion: "10.0.26220",
	CpuCores:  20,
	MemoryGB:  24,
}

const (
	desktopInfoFileTTL    = 5 * time.Minute
	desktopInfoFileMaxLen = 4096
)

type desktopInfoCacheEntry struct {
	value  desktopInfo
	ok     bool
	readAt time.Time
}

var (
	desktopInfoMu    sync.Mutex
	desktopInfoCache = map[string]desktopInfoCacheEntry{}
)

// resolveDesktopInfo 生效的硬件指纹：Client.DesktopInfoFile 文件源 > 内置默认。
func (c *Client) resolveDesktopInfo() desktopInfo {
	if c != nil && c.DesktopInfoFile != "" {
		if info, ok := readDesktopInfoFile(c.DesktopInfoFile); ok {
			return info
		}
	}
	return desktopInfoFallback
}

// readDesktopInfoFile 读并缓存硬件信息文件（5 分钟 TTL；读失败/解析失败/校验
// 失败同样缓存失败结果，防每请求重读坏文件）。
func readDesktopInfoFile(path string) (desktopInfo, bool) {
	desktopInfoMu.Lock()
	defer desktopInfoMu.Unlock()
	if e, ok := desktopInfoCache[path]; ok && time.Since(e.readAt) < desktopInfoFileTTL {
		return e.value, e.ok
	}
	raw, err := os.ReadFile(path)
	info, valid := desktopInfo{}, false
	if err == nil && len(raw) <= desktopInfoFileMaxLen {
		info, valid = parseDesktopInfo(raw)
	}
	desktopInfoCache[path] = desktopInfoCacheEntry{value: info, ok: valid, readAt: time.Now()}
	return info, valid
}

// parseDesktopInfo 解析并整体校验：任一字段缺失/越界即整体拒绝（不部分生效）。
func parseDesktopInfo(raw []byte) (desktopInfo, bool) {
	var info desktopInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return desktopInfo{}, false
	}
	switch {
	case info.Os == "" || len(info.Os) > 32,
		info.Arch == "" || len(info.Arch) > 32,
		info.OsVersion == "" || len(info.OsVersion) > 64,
		info.CpuCores < 1 || info.CpuCores > 1024,
		info.MemoryGB < 1 || info.MemoryGB > 4096:
		return desktopInfo{}, false
	}
	return info, true
}
