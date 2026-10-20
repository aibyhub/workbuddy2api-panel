// upstream_timeout_test.go 钉住 isUpstreamTimeout 的三态判定口径：超时和
// "网络抖动"此前共用同一条换号路径——注定超时的请求会轮转 MaxRotate 次，
// 期间还给一串健康号喂连败计数。移植自 OkRoromori 分支（七例对照）。
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

type timeoutErr struct{ timeout bool }

func (e timeoutErr) Error() string   { return "stub" }
func (e timeoutErr) Timeout() bool   { return e.timeout }
func (e timeoutErr) Temporary() bool { return e.timeout }

func TestIsUpstreamTimeout(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		clientGone bool
		want       bool
	}{
		{"net.Error 超时", timeoutErr{true}, false, true},
		{"net.Error 非超时（连接被拒）", timeoutErr{false}, false, false},
		{"显式 deadline", fmt.Errorf("read: %w", context.DeadlineExceeded), false, true},
		{"客户端断连（ctx 已取消）", context.Canceled, true, false},
		{"空闲看门狗掐流（客户端仍在）", context.Canceled, false, true},
		{"普通错误", errors.New("boom"), false, false},
		{"nil", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUpstreamTimeout(c.err, c.clientGone); got != c.want {
				t.Fatalf("isUpstreamTimeout(%v, clientGone=%v) = %v, want %v", c.err, c.clientGone, got, c.want)
			}
		})
	}

	// 保证 stub 真的实现了 net.Error（否则上面的用例会静默走不到 Timeout 分支）
	var ne net.Error = timeoutErr{true}
	_ = ne
}

// testTimeoutUpstream 构造按 Authorization 区分行为的 fake：超名单里的号返回
// 传输层超时，其余返回 200 SSE。
func testTimeoutUpstream(t *testing.T, timeoutAuths map[string]bool, hit map[string]int) *upstream.Client {
	t.Helper()
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			authz := r.Header.Get("Authorization")
			hit[authz]++
			if timeoutAuths[authz] {
				return nil, timeoutErr{true}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sseOK)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

// TestUpstreamTimeoutExtraRotationSecondLuck ??(cmd Default ?? 1):
// ???? ? ?????? ? ??????????? 1 ?,?? 200?
func TestUpstreamTimeoutExtraRotationSecondLuck(t *testing.T) {
	hit := map[string]int{}
	up := testTimeoutUpstream(t, map[string]bool{"Bearer at-bad": true}, hit)
	p := testPoolWith(
		&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "ok", AccessToken: "at-ok", ExpiresAt: 9999999999},
	)
	p.SetCredits("bad", 2000, 0) // ???:?? bad
	p.SetCredits("ok", 1000, 0)
	h := NewHandler(Config{Pool: p, Upstream: up, TimeoutRotateExtra: 1})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s (extra rotation must give the second account a chance)", rec.Code, rec.Body)
	}
	if hit["Bearer at-bad"] != 1 || hit["Bearer at-ok"] != 1 {
		t.Errorf("calls=%v want bad=1 ok=1", hit)
	}
	// ???????:??????/??/?????
	for _, uid := range []string{"bad", "ok"} {
		if st, _ := p.Status(uid); st.Cooling || st.Disabled || st.BreakerFails != 0 {
			t.Errorf("uid=%s penalized by timeout path: %+v", uid, st)
		}
	}
}

// TestUpstreamTimeoutExtraBudgetExhausted ??????:extra=1??????
// ? ??????????????? ? ?? 2 ??????503 ?? upstream_timeout?
func TestUpstreamTimeoutExtraBudgetExhausted(t *testing.T) {
	hit := map[string]int{}
	up := testTimeoutUpstream(t, map[string]bool{"Bearer at-bad": true, "Bearer at-ok": true}, hit)
	p := testPoolWith(
		&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "ok", AccessToken: "at-ok", ExpiresAt: 9999999999},
	)
	p.SetCredits("bad", 2000, 0)
	p.SetCredits("ok", 1000, 0)
	h := NewHandler(Config{Pool: p, Upstream: up, TimeoutRotateExtra: 1})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "upstream timed out") {
		t.Fatalf("body=%s", rec.Body)
	}
	if hit["Bearer at-bad"] != 1 || hit["Bearer at-ok"] != 1 {
		t.Errorf("calls=%v want exactly one hop per account (budget exhausted)", hit)
	}
}

// TestUpstreamTimeoutExtraDisabledZero extra=0(???/???)=
// ??????:????????,?? 1 ????503?
func TestUpstreamTimeoutExtraDisabledZero(t *testing.T) {
	hit := map[string]int{}
	up := testTimeoutUpstream(t, map[string]bool{"Bearer at-bad": true}, hit)
	p := testPoolWith(
		&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "ok", AccessToken: "at-ok", ExpiresAt: 9999999999},
	)
	p.SetCredits("bad", 2000, 0)
	p.SetCredits("ok", 1000, 0)
	h := NewHandler(Config{Pool: p, Upstream: up}) // TimeoutRotateExtra ?? = ?
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503", rec.Code)
	}
	if hit["Bearer at-bad"] != 1 || hit["Bearer at-ok"] != 0 {
		t.Errorf("calls=%v want bad=1 ok=0 (rotation stopped)", hit)
	}
}
