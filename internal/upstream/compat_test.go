// compat_test.go OpenAI 兼容面回归：非流式 usage 恒输出；缓存命中统计的 hit=0
// 计入口径（键存在即有效）；normalizeUsageCacheAliases 对 0 值不回写。
package upstream

import (
	"strings"
	"testing"
)

func TestAggregateAlwaysEmitsUsage(t *testing.T) {
	// 上游整流无 usage 帧：聚合响应仍带零值 usage（严格 schema 客户端按必填解析）。
	raw := "data: " + `{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n" +
		"data: " + `{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	resp, err := Aggregate(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage key missing: %v", resp)
	}
	if usage["total_tokens"].(float64) != 0 || usage["prompt_tokens"].(float64) != 0 {
		t.Fatalf("zero usage expected, got %v", usage)
	}
}

func TestUsageCacheHitTokensAcceptsZero(t *testing.T) {
	// 上游显式回 hit=0：统计层必须计入（否则全 miss 请求被排除、命中率虚高）。
	usage := map[string]any{"prompt_cache_hit_tokens": float64(0), "prompt_tokens": float64(120)}
	hit, ok := UsageCacheHitTokens(usage)
	if !ok || hit != 0 {
		t.Fatalf("hit=0 must be accepted: hit=%v ok=%v", hit, ok)
	}
	miss, ok := UsageCacheMissTokens(usage)
	if !ok || miss != 120 {
		t.Fatalf("miss derivation with hit=0: miss=%v ok=%v", miss, ok)
	}
	// 键完全缺失仍是 ok=false。
	if _, ok := UsageCacheHitTokens(map[string]any{"prompt_tokens": float64(1)}); ok {
		t.Fatal("missing alias keys must be ok=false")
	}
}

func TestNormalizeUsageCacheAliasesLeavesZeroUntouched(t *testing.T) {
	usage := map[string]any{"cached_tokens": float64(0), "prompt_tokens": float64(5)}
	out := normalizeUsageCacheAliases(usage)
	if len(out) != len(usage) {
		t.Fatalf("zero hit must not be rewritten into other aliases: %v", out)
	}
	if _, ok := out["prompt_tokens_details"]; ok {
		t.Fatal("zero hit must not invent prompt_tokens_details")
	}
}
