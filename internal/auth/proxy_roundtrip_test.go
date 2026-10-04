// proxy_roundtrip_test.go 账号级出口代理的落盘/加载 roundtrip（双形态解析）。
package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthProxyURLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workbuddy-u1.json")
	a := &Auth{
		AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999,
		UID: "u1", FilePath: path,
		ProxyURL: "socks5://user:pass@10.0.0.1:10529",
	}
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 嵌套形落盘：顶层 proxy_url 键。
	raw := readFileT(t, path)
	if !strings.Contains(raw, `"proxy_url": "socks5://user:pass@10.0.0.1:10529"`) {
		t.Fatalf("proxy_url not persisted:\n%s", raw)
	}
	// 重新解析（嵌套形）拿到同值；加锁访问器一致。
	b, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	b.FilePath = path
	if b.ProxyURLValue() != "socks5://user:pass@10.0.0.1:10529" {
		t.Fatalf("roundtrip mismatch: %q", b.ProxyURLValue())
	}
	// 清空后不再落盘该键（旧文件不引入空键）。
	b.SetProxyURL("")
	if err := b.SaveAtomic(); err != nil {
		t.Fatalf("save cleared: %v", err)
	}
	raw2 := readFileT(t, path)
	if strings.Contains(raw2, "proxy_url") {
		t.Fatalf("empty proxy_url should not be persisted:\n%s", raw2)
	}
}

func TestAuthProxyURLFlatParse(t *testing.T) {
	a, err := Parse([]byte(`{"accessToken":"at","uid":"u2","proxy_url":"http://10.0.0.2:8080"}`))
	if err != nil {
		t.Fatalf("parse flat: %v", err)
	}
	if a.ProxyURLValue() != "http://10.0.0.2:8080" {
		t.Fatalf("flat proxy_url: %q", a.ProxyURLValue())
	}
	if !a.SetProxyURL("socks5h://10.0.0.3:1080") {
		t.Fatal("SetProxyURL should report change")
	}
	if a.SetProxyURL("socks5h://10.0.0.3:1080") {
		t.Fatal("SetProxyURL same value should report no change")
	}
	if a.ProxyURLValue() != "socks5h://10.0.0.3:1080" {
		t.Fatalf("after set: %q", a.ProxyURLValue())
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
