package server

// hang_test.go 模型级挂死熔断（hang.go）测试：单元口径（观察窗爬升/成功清零/
// 保持期放行）+ 端到端口径（transport 故障记窗→熔断→入口 fast-fail 503）。
// MaxRotate=1（不轮换）保证两笔停滞 = 恰好两笔上游调用、零退避。

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// errHangFakeTimeout 让 errors.As 捕到 net.Error 且 Timeout()==true（表头超时口径）。
type errHangFakeTimeout struct{}

func (errHangFakeTimeout) Error() string   { return "i/o timeout (fake)" }
func (errHangFakeTimeout) Timeout() bool   { return true }
func (errHangFakeTimeout) Temporary() bool { return false }

func newHangTestHandlerFull(t *testing.T, up *upstream.Client, maxRotate int) *Handler {
	t.Helper()
	return NewHandler(Config{
		Pool:      testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:  up,
		MaxRotate: maxRotate,
	})
}

func newHangTestHandler(t *testing.T, maxRotate int, behavior func(auth string) (int, string, bool)) *Handler {
	t.Helper()
	return newHangTestHandlerFull(t, newFakeUpstream(t, behavior), maxRotate)
}

// TestHangGuardUnitRamp 单元口径：第一笔停滞不熔断（单发抖动不熔断），第二笔
// 窗内熔断；成功一笔清零；保持期过后自动放行；异模型互不连坐。
func TestHangGuardUnitRamp(t *testing.T) {
	h := newHangTestHandler(t, 3, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	if h.hangBlocked("glm-5.2") {
		t.Fatal("cold guard must not be engaged")
	}

	h.noteModelHang("glm-5.2")
	if h.hangBlocked("glm-5.2") {
		t.Fatal("single stall must not trip the guard (threshold=2 requests)")
	}

	h.noteModelHang("glm-5.2")
	if !h.hangBlocked("glm-5.2") {
		t.Fatal("second stall within window must trip the guard")
	}

	// 保持期过后自动放行。
	h.hangMu.Lock()
	h.hangUntil["glm-5.2"] = time.Now().Add(-time.Millisecond)
	h.hangMu.Unlock()
	if h.hangBlocked("glm-5.2") {
		t.Fatal("expired hold must release the model")
	}

	// 成功一笔即清零：既清观察窗也清熔断。
	h.noteModelHang("glm-5.2")
	h.noteModelHang("glm-5.2")
	h.noteModelOK("glm-5.2")
	if h.hangBlocked("glm-5.2") {
		t.Fatal("success must clear the guard")
	}
	if hits := len(h.hangHits["glm-5.2"]); hits != 0 {
		t.Fatalf("success must reset the observation window, got %d hits", hits)
	}

	// 不同模型互不连坐。
	h.noteModelHang("glm-5.2")
	h.noteModelHang("glm-5.2")
	if h.hangBlocked("deepseek-v4.1") {
		t.Fatal("other models must not inherit the guard")
	}
}

// TestHangGuardFastFailsEntry 端到端：上游 transport error 两笔独立请求（每笔
// 恰好一次上游调用）→ 熔断 → 第三笔入口 fast-fail 503 且上游零新增调用。
func TestHangGuardFastFailsEntry(t *testing.T) {
	calls := atomic.Int64{}
	up := &upstream.Client{ // transport error（无分类信封）→ 抖动分支记窗
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, &net.OpError{Op: "read", Err: errHangFakeTimeout{}}
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
	h := newHangTestHandlerFull(t, up, 1)

	for i := 0; i < 2; i++ { // 两笔独立请求：第一笔记窗，第二笔触发熔断
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls=%d want 2 (MaxRotate=1, one call per request)", got)
	}
	if !h.hangBlocked("glm-5.2") {
		t.Fatal("two stalled requests must engage the guard")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s (want 503 fast-fail while in hold)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "model_temporarily_down") {
		t.Errorf("body should carry model_temporarily_down: %s", rec.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls=%d want 2 (third request must fail fast at entry)", got)
	}
}

// TestHangGuardOKClears 端到端：熔断保持期内请求入口 503；保持期过期后放行，
// 一笔成功清零观察窗。
func TestHangGuardOKClears(t *testing.T) {
	h := newHangTestHandler(t, 3, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	h.noteModelHang("glm-5.2")
	h.noteModelHang("glm-5.2")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d (want 503: request during hold rejected at entry)", rec.Code)
	}

	// 保持期过期 → 放行 → 成功流经透传 → 守卫清除。
	h.hangMu.Lock()
	h.hangUntil["glm-5.2"] = time.Now().Add(-time.Millisecond)
	h.hangMu.Unlock()
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s (want 200 after hold expiry)", rec2.Code, rec2.Body)
	}
	if h.hangBlocked("glm-5.2") {
		t.Fatal("successful pass-through must clear the guard")
	}
}
