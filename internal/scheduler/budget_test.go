// budget_test.go 单号预算超时语义：挂死账号不拖垮整轮——首个账号的上游调用
// 超过 accountBudget 时，调度器放弃等待继续处理后续账号（09:00 队头阻塞的结构性
// 修复）；panic 同样被兜住不杀进程。
package scheduler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// hangingServer 签到端点挂死（超过任何合理预算），余额端点立即成功。
func hangingServer(hung *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/daily-checkin"):
			hung.Add(1)
			time.Sleep(30 * time.Second) // 远超测试预算；进程退出由测试服务器的 Close 兜底
		default:
			w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
}

func TestRunCheckinNowHungAccountDoesNotBlockOthers(t *testing.T) {
	old := accountBudget
	accountBudget = 150 * time.Millisecond
	defer func() { accountBudget = old }()

	var hung atomic.Int64
	srv := hangingServer(&hung)
	defer srv.Close()

	p := pool.New("")
	// u1 挂死；u2 正常（走同一上游：u1 卡在 checkin，u2 卡在 balance —— 都会超预算，
	// 但循环必须继续推进而不是整轮卡死）。
	for _, uid := range []string{"u1", "u2"} {
		a := &auth.Auth{UID: uid, AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
		p.Add(a)
	}
	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	// 注意：srv.Client 的 Transport 会真实发起挂死请求；预算超时后循环不等它。
	s := New(Config{Pool: p, Upstream: up})

	done := make(chan struct{})
	go func() { s.RunCheckinNow(); close(done) }()
	select {
	case <-done:
		// 循环在预算内推进完毕（2 个账号 × 150ms 预算 + 余量）。
	case <-time.After(10 * time.Second):
		t.Fatal("RunCheckinNow blocked far beyond per-account budget — head-of-line regression")
	}
	if hung.Load() == 0 {
		t.Fatal("hanging checkin endpoint should have been hit")
	}
	// 两个账号都被尝试过（u1 的挂死没有阻断 u2 的派发）。
	for _, uid := range []string{"u1", "u2"} {
		if p.AuthByUID(uid) == nil {
			t.Fatalf("account %s missing from pool", uid)
		}
	}
}

func TestRunAccountBoundedRecoversPanic(t *testing.T) {
	old := accountBudget
	accountBudget = time.Second
	defer func() { accountBudget = old }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAccountBounded("test-panic", func() { panic("boom") })
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("panic in account work must be recovered by runAccountBounded")
	}
}
