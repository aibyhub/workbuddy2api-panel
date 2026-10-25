// desktop_info_test.go 硬件指纹文件源测试（2026-10-25 审计修复项）。
package upstream

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestParseDesktopInfo 合法文件解析 + 任一字段非法整体拒绝。
func TestParseDesktopInfo(t *testing.T) {
	ok, valid := parseDesktopInfo([]byte(`{"os":"win32","arch":"x64","osVersion":"10.0.26200","cpuCores":12,"memorySize":16}`))
	if !valid || ok.CpuCores != 12 || ok.MemoryGB != 16 || ok.OsVersion != "10.0.26200" {
		t.Fatalf("valid file rejected: %+v ok=%v", ok, valid)
	}
	bad := []string{
		`{"os":"win32","arch":"x64","osVersion":"10.0.26200","cpuCores":0,"memorySize":16}`,    // cpu 越界
		`{"os":"win32","arch":"x64","osVersion":"10.0.26200","cpuCores":12,"memorySize":9999}`, // mem 越界
		`{"os":"","arch":"x64","osVersion":"1","cpuCores":12,"memorySize":16}`,                 // os 缺失
		`not-json`,
		`{}`,
	}
	for _, b := range bad {
		if _, valid := parseDesktopInfo([]byte(b)); valid {
			t.Errorf("invalid content accepted: %s", b)
		}
	}
}

// TestResolveDesktopInfoFileSource 文件源生效 + 坏文件回落内置默认。
func TestResolveDesktopInfoFileSource(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "device-info.json")
	_ = os.WriteFile(fp, []byte(`{"os":"win32","arch":"x64","osVersion":"10.0.26200","cpuCores":12,"memorySize":16}`), 0o600)

	got := (&Client{DesktopInfoFile: fp}).resolveDesktopInfo()
	if got.CpuCores != 12 || got.MemoryGB != 16 {
		t.Fatalf("file source not applied: %+v", got)
	}

	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"cpuCores":9999}`), 0o600)
	fb := (&Client{DesktopInfoFile: bad}).resolveDesktopInfo()
	if fb != desktopInfoFallback {
		t.Fatalf("bad file should fall back to default, got %+v", fb)
	}

	// 空配置 = 默认
	if (&Client{}).resolveDesktopInfo() != desktopInfoFallback {
		t.Fatal("empty config should use fallback")
	}
}

// TestDesktopFingerprintUsesHardware 指纹的硬件四件套来自入参（文件源上游已解析），
// 确保不再有硬编码 20/24/26220 泄漏到事件体。
func TestDesktopFingerprintUsesHardware(t *testing.T) {
	a := &auth.Auth{UID: "u1", Nickname: "n"}
	hw := desktopInfo{Os: "win32", Arch: "x64", OsVersion: "10.0.26200", CpuCores: 12, MemoryGB: 16}
	fp := desktopFingerprint(a, "q36", hw)
	if fp["cpuCores"] != 12 || fp["memorySize"] != 16 || fp["osVersion"] != "10.0.26200" {
		t.Fatalf("hardware not applied: %v %v %v", fp["cpuCores"], fp["memorySize"], fp["osVersion"])
	}
	if fp["ideVersion"] != desktopVersion || fp["extVersion"] != desktopVersion {
		t.Fatalf("version fields stale: %v/%v", fp["ideVersion"], fp["extVersion"])
	}
	if fp["qimei36"] != "q36" {
		t.Fatal("qimei36 not injected")
	}
}
