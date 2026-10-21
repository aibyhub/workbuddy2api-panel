package server

import (
	"reflect"
	"testing"
)

// TestRealmChain 钉死跨域兜底序列语义（2026-10-21 glm-5.3 事故修复）：
//   - 裸名 → [主域, 兜底域]（主域选不出号时允许跨域承接）；
//   - 显式 cn:/global: 前缀 → 严格单域（调用方明确点名了域，不跨）；
//   - 兜底域空 / 等于主域 → 恒单域（机制关闭）；
//   - 冒号在首/空前缀（":x"，"weird:"）都视作裸名（与 resolveModel 回落一致）。
func TestRealmChain(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		primary string
		fb      string
		want    []string
	}{
		{"bare enables fallback", "glm-5.3", "cn", "global", []string{"cn", "global"}},
		{"bare fb empty single", "glm-5.3", "cn", "", []string{"cn"}},
		{"bare fb equals primary", "glm-5.3", "cn", "cn", []string{"cn"}},
		{"explicit cn strict", "cn:glm-5.3", "cn", "global", []string{"cn"}},
		{"explicit global strict", "global:glm-5.3", "global", "cn", []string{"global"}},
		{"bare global primary also chainable", "glm-5.3", "global", "cn", []string{"global", "cn"}},
		{"colon-first is bare", ":glm", "cn", "global", []string{"cn", "global"}},
		{"weird prefix is bare", "arg:glm", "cn", "global", []string{"cn", "global"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := realmChain(c.model, c.primary, c.fb)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("realmChain(%q,%q,%q)=%v want %v", c.model, c.primary, c.fb, got, c.want)
			}
		})
	}
}

// TestRealmInChain 粘性判定谓词：空串域恒不在链内（与旧 `realm != ""` 守卫对齐，
// 空域账号不该被误选入已指定域的请求序列）。
func TestRealmInChain(t *testing.T) {
	chain := []string{"cn", "global"}
	if !realmInChain("cn", chain) || !realmInChain("global", chain) {
		t.Fatal("membership broken")
	}
	if realmInChain("eu", chain) {
		t.Fatal("foreign realm accepted")
	}
	if realmInChain("", chain) {
		t.Fatal("empty realm must not match")
	}
}
