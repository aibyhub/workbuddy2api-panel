// proxypool_test.go 代理池存储层测试：CRUD / 按名解析 / 批量导入 / 健康缓存 /
// 均衡分配 / 脱敏。HTTP 端点是薄封装（withAuth + writeJSON），逻辑全在存储层。
package panel

import (
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func newTestProxyPool(t *testing.T) *ProxyPool {
	t.Helper()
	return NewProxyPool(filepath.Join(t.TempDir(), "proxies.json"))
}

func TestProxyPoolAddResolveRemove(t *testing.T) {
	p := newTestProxyPool(t)
	if err := p.Add("do-sg", "socks5://user:pass@10.0.0.1:10529"); err != nil {
		t.Fatalf("add: %v", err)
	}
	// 重复名 / 重复地址 / 非法地址拒绝
	if err := p.Add("do-sg", "http://1.2.3.4:8080"); err == nil {
		t.Fatal("duplicate name should fail")
	}
	if err := p.Add("other", "socks5://user:pass@10.0.0.1:10529"); err == nil {
		t.Fatal("duplicate url should fail")
	}
	if err := p.Add("bad", "ftp://1.2.3.4:21"); err == nil {
		t.Fatal("invalid scheme should fail")
	}
	if err := p.Add("", "http://1.2.3.4:8080"); err == nil {
		t.Fatal("empty name should fail")
	}
	u, ok := p.Resolve("do-sg")
	if !ok || u != "socks5://user:pass@10.0.0.1:10529" {
		t.Fatalf("resolve: ok=%v u=%q", ok, u)
	}
	if _, ok := p.Resolve("nope"); ok {
		t.Fatal("unknown name should not resolve")
	}
	if got := p.NameFor("socks5://user:pass@10.0.0.1:10529"); got != "do-sg" {
		t.Fatalf("NameFor: %q", got)
	}
	if got := p.NameFor("http://not-in-pool:1"); got != "" {
		t.Fatalf("NameFor unknown: %q", got)
	}
	if !p.Remove("do-sg") {
		t.Fatal("remove should succeed")
	}
	if p.Remove("do-sg") {
		t.Fatal("double remove should fail")
	}
	if _, ok := p.Resolve("do-sg"); ok {
		t.Fatal("removed entry should not resolve")
	}
}

func TestProxyPoolPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxies.json")
	p := NewProxyPool(path)
	if err := p.Add("a", "http://1.1.1.1:80"); err != nil {
		t.Fatalf("add: %v", err)
	}
	// 重新加载：条目仍在（原子落盘）。
	p2 := NewProxyPool(path)
	if _, ok := p2.Resolve("a"); !ok {
		t.Fatal("entry lost after reload")
	}
	// 文件不存在 = 空池不报错。
	if p3 := NewProxyPool(filepath.Join(dir, "none.json")); len(p3.All()) != 0 {
		t.Fatal("missing file should yield empty pool")
	}
}

func TestProxyPoolUpdateAndBulk(t *testing.T) {
	p := newTestProxyPool(t)
	_ = p.Add("old", "http://1.1.1.1:80")
	up, err := p.Update("old", ProxyUpdate{Name: "new", URL: "socks5://2.2.2.2:1080"})
	if err != nil || up.Name != "new" || up.URL != "socks5://2.2.2.2:1080" {
		t.Fatalf("update: %v %+v", err, up)
	}
	if _, ok := p.Resolve("old"); ok {
		t.Fatal("old name should be gone")
	}
	// 地址变了探测缓存清零；启用位显式关闭。
	p.SetHealth("new", upstream.ProxyProbeResult{OK: true, ExitIP: "2.2.2.2", Status: 404, LatencyMs: 12})
	disabled := false
	if _, err := p.Update("new", ProxyUpdate{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	all := p.All()
	if len(all) != 1 || all[0].IsEnabled() {
		t.Fatalf("enabled flag: %+v", all)
	}

	added, failed := p.AddBulk("# 注释\n\nhk http://3.3.3.3:80\nbad-line-not-url\n=「」\nhttp://4.4.4.4:80")
	if len(added) != 2 || added[0] != "hk" {
		t.Fatalf("bulk added: %v", added)
	}
	if len(failed) != 2 {
		t.Fatalf("bulk failed: %v", failed)
	}
	// 非法行也占用自动命名序号：两条失败行走掉 代理1/代理2，地址行走 代理3。
	if _, ok := p.Resolve("代理3"); !ok {
		t.Fatal("auto-named entry missing")
	}
}

func TestProxyPoolLeastUsed(t *testing.T) {
	p := newTestProxyPool(t)
	_ = p.Add("a", "http://1.1.1.1:80")
	_ = p.Add("b", "http://2.2.2.2:80")
	disabled := false
	_, _ = p.Update("b", ProxyUpdate{Enabled: &disabled})
	// b 停用：即使占用为 0 也不参与分配。
	got, ok := p.LeastUsed(map[string]int{"http://1.1.1.1:80": 3})
	if !ok || got != "http://1.1.1.1:80" {
		t.Fatalf("least used: ok=%v got=%q", ok, got)
	}
	// 空池。
	if _, ok := (newTestProxyPool(t)).LeastUsed(nil); ok {
		t.Fatal("empty pool should not allocate")
	}
}

func TestMaskProxyAndValidProxyURL(t *testing.T) {
	if got := maskProxy("socks5://user:secret@10.0.0.1:10529"); got != "socks5://***@10.0.0.1:10529" {
		t.Fatalf("mask with userinfo: %q", got)
	}
	if got := maskProxy("http://10.0.0.2:8080"); got != "http://10.0.0.2:8080" {
		t.Fatalf("mask without userinfo: %q", got)
	}
	if got := maskProxy(""); got != "" {
		t.Fatalf("mask empty: %q", got)
	}
	for _, ok := range []string{"http://a:1", "https://a:1", "socks5://a:1", "socks5h://a:1"} {
		if !upstream.ValidProxyURL(ok) {
			t.Fatalf("ValidProxyURL(%q) = false", ok)
		}
	}
	for _, bad := range []string{"ftp://a:1", "a:1", "", "http://"} {
		if upstream.ValidProxyURL(bad) {
			t.Fatalf("ValidProxyURL(%q) = true", bad)
		}
	}
}
