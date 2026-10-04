// compat_test.go OpenAI 兼容回归：错误 type 按状态码映射；缓存统计的假 100% 样本消除。
package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIErrorTypeMapping(t *testing.T) {
	cases := map[int]string{
		400: "invalid_request_error",
		401: "invalid_request_error",
		404: "invalid_request_error",
		429: "rate_limit_error",
		500: "api_error",
		502: "api_error",
		503: "api_error",
	}
	for status, want := range cases {
		if got := openAIErrorType(status); got != want {
			t.Errorf("openAIErrorType(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestWriteOpenAIErrorShape(t *testing.T) {
	w := httptest.NewRecorder()
	writeOpenAIError(w, 429, "rate_limit_exceeded", "slow down")
	body := w.Body.String()
	for _, want := range []string{`"type":"rate_limit_error"`, `"code":"rate_limit_exceeded"`, `"message":"slow down"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
	w2 := httptest.NewRecorder()
	writeOpenAIErrorHint(w2, 401, "invalid_api_key", "bad key", "")
	if !strings.Contains(w2.Body.String(), `"type":"invalid_request_error"`) {
		t.Errorf("hint variant must map type too: %s", w2.Body.String())
	}
}

func TestChatStatsCacheTokensNoFakeFullHit(t *testing.T) {
	// hit 有值但 miss 无法推导（缺 prompt）：旧实现返回 (hit,0,true) → 假 100% 命中
	// 样本污染统计；现在整体 ok=false 排除。
	s := &chatStatsReader{hasCacheHit: true, cacheHit: 365568}
	if _, _, ok := s.CacheTokens(); ok {
		t.Fatal("hit without derivable miss must be excluded (ok=false)")
	}
	// prompt 可推导：miss = prompt - hit。
	s2 := &chatStatsReader{hasCacheHit: true, cacheHit: 100, hasPromptTokens: true, promptTokens: 300}
	hit, miss, ok := s2.CacheTokens()
	if !ok || hit != 100 || miss != 200 {
		t.Fatalf("derived miss: hit=%d miss=%d ok=%v", hit, miss, ok)
	}
	// 上游显式 miss 优先。
	s3 := &chatStatsReader{hasCacheHit: true, cacheHit: 100, hasCacheMiss: true, cacheMiss: 40}
	hit, miss, ok = s3.CacheTokens()
	if !ok || hit != 100 || miss != 40 {
		t.Fatalf("explicit miss: hit=%d miss=%d ok=%v", hit, miss, ok)
	}
	// prompt < hit（不可信）：排除。
	s4 := &chatStatsReader{hasCacheHit: true, cacheHit: 500, hasPromptTokens: true, promptTokens: 300}
	if _, _, ok := s4.CacheTokens(); ok {
		t.Fatal("prompt < hit must be excluded")
	}
}
