package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// realmFallbackTestPool 构建 cn/global 两域账号的池（2026-10-21 事故拓扑缩小版）：
//   - cnb：cn 域账号，credits 2000（确定性随机源 → 第一轮必选它）；
//   - glb：global 域账号，credits 1000（主域空转时的跨域兜底承接方）。
func realmFallbackTestPool(t *testing.T) *pool.Pool {
	t.Helper()
	cnb := &auth.Auth{UID: "cnb", AccessToken: "at-cnb", ExpiresAt: 9999999999}
	glb := &auth.Auth{UID: "glb", AccessToken: "at-glb", ExpiresAt: 9999999999}
	if _, err := auth.BackfillRealmFor(cnb, "cn"); err != nil {
		t.Fatalf("backfill cnb: %v", err)
	}
	if _, err := auth.BackfillRealmFor(glb, "global"); err != nil {
		t.Fatalf("backfill glb: %v", err)
	}
	p := testPoolWith(cnb, glb)
	p.SetCredits("cnb", 2000, 0)
	p.SetCredits("glb", 1000, 0)
	return p
}

// TestChatBareModelCrossRealmFallback 裸名 glm-5.3：cn 账号 6004 限额（带重置时间）
// → 主域 cn 对该模型再无可选账号 → 跨域兜底由 global 账号承接成功。
// 断言面：①整体 200；②cnb 只进模型级冷却（账号级不 cooling，与 6004 语义一致）；
// ③pick 口径主域空 / 兜底域健在；④第二次同款请求继续由兜底域稳定承接。
func TestChatBareModelCrossRealmFallback(t *testing.T) {
	reset := time.Now().Add(30 * time.Minute)
	ts := reset.In(upstream.SoftRateResetLoc()).Format("2006-01-02 15:04:05")
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if authz == "Bearer at-cnb" {
			return 429, `{"code":6004,"msg":"您的使用量已超出频率限制，将在 ` + ts + ` UTC+8 重置"}`, false
		}
		return 200, sseOK, true
	})
	p := realmFallbackTestPool(t)
	h := NewHandler(Config{Pool: p, Upstream: up, CrossRealmFallback: 1, CrossRealmFallbackRealm: "global"})
	do := func() *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"glm-5.3","messages":[]}`)))
		return rec
	}
	rec := do()
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s (want fallback to global)", rec.Code, rec.Body)
	}
	// cnb：模型级独立冷却台账单行，账号级不 cooling（6004 语义，账号其余模型可用）。
	st, _ := p.Status("cnb")
	if st.Cooling {
		t.Fatalf("cnb should NOT be account-level cooling: %+v", st)
	}
	if len(st.RateLimitedModels) != 1 || st.RateLimitedModels[0].Model != "glm-5.3" {
		t.Fatalf("cnb rate-limited models=%+v want single glm-5.3 row", st.RateLimitedModels)
	}
	// 选号口径：主域 cn 对 glm-5.3 全空（cnb 冷却中），兜底域 global 健在。
	if a := p.PickExcludingForRealm(nil, "glm-5.3", "cn"); a != nil {
		t.Fatalf("primary cn pick should be empty, got %s", a.UID)
	}
	if a := p.PickExcludingForRealm(nil, "glm-5.3", "global"); a == nil || a.UID != "glb" {
		t.Fatalf("fallback global pick want glb, got %+v", a)
	}
	// 第二次请求：cn 冷却仍在 → 继续走 global，稳定 200。
	if rec2 := do(); rec2.Code != 200 {
		t.Fatalf("second request code=%d body=%s (want stable 200)", rec2.Code, rec2.Body)
	}
}

// TestChatExplicitCNPrefixNeverCrosses 显式 cn:glm-5.3 严格单域：跨域兜底永不旁路
// 显式域意图。cn 池 6004 冷却 → 模型级冷却在全池口径不算 Blocked（glb 健康能服务）
// → lastErr 保留 6004 原文，最终按 ErrSoftRate 出 429 rate_limit_exceeded；
// 关键断言是 global 账号零调用（cntGlb==0）。
//
// 注：这不是巧合，而是 mock 拓扑的必然——pick 只出 cn 域账号；glb 从未被选中，
// 就不可能产生以 glb 名义的上游调用（计费回传也以成功请求为前提）。
func TestChatExplicitCNPrefixNeverCrosses(t *testing.T) {
	reset := time.Now().Add(30 * time.Minute)
	ts := reset.In(upstream.SoftRateResetLoc()).Format("2006-01-02 15:04:05")
	var glbCalls int
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if authz == "Bearer at-cnb" {
			return 429, `{"code":6004,"msg":"您的使用量已超出频率限制，将在 ` + ts + ` UTC+8 重置"}`, false
		}
		glbCalls++
		return 200, sseOK, true
	})
	p := realmFallbackTestPool(t)
	h := NewHandler(Config{Pool: p, Upstream: up, CrossRealmFallback: 1, CrossRealmFallbackRealm: "global"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"cn:glm-5.3","messages":[]}`)))
	if rec.Code != 429 {
		t.Fatalf("code=%d body=%s (want ErrSoftRate 429, no realm cross)", rec.Code, rec.Body)
	}
	var e map[string]any
	json.Unmarshal(rec.Body.Bytes(), &e)
	if e["error"] == nil {
		t.Errorf("want error envelope: %s", rec.Body)
	}
	if glbCalls != 0 {
		t.Fatalf("global account called %d times, want 0 (strict explicit realm)", glbCalls)
	}
}

// TestChatCrossRealmFallbackDisabled 开关关闭（CrossRealmFallback=0）→ 裸名请求
// 严格旧行为：主域（cn）6004 冷却 → 429（6004 原文透传），global 账号零调用。
func TestChatCrossRealmFallbackDisabled(t *testing.T) {
	reset := time.Now().Add(30 * time.Minute)
	ts := reset.In(upstream.SoftRateResetLoc()).Format("2006-01-02 15:04:05")
	var glbCalls int
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if authz == "Bearer at-cnb" {
			return 429, `{"code":6004,"msg":"您的使用量已超出频率限制，将在 ` + ts + ` UTC+8 重置"}`, false
		}
		glbCalls++
		return 200, sseOK, true
	})
	p := realmFallbackTestPool(t)
	h := NewHandler(Config{Pool: p, Upstream: up, CrossRealmFallback: 0, CrossRealmFallbackRealm: "global"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.3","messages":[]}`)))
	if rec.Code != 429 {
		t.Fatalf("code=%d body=%s (want legacy 429, no realm cross)", rec.Code, rec.Body)
	}
	if glbCalls != 0 {
		t.Fatalf("global account called %d times, want 0 (fallback disabled)", glbCalls)
	}
}
