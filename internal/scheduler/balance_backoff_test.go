// 后台余额刷新的错峰与连败退避测试（RunBalanceRefreshStaggered 语义）：
//   - 连败 ≥3 → 本轮跳过（上游零调用），退避时长 = (k-2)×interval/2，封顶 2×interval；
//   - 成功 → 状态清零；
//   - interval=0（手动路径）只计数、不产生跳过、永不拦截上游调用；
//   - 错峰偏移确定且 ∈ [0, spread)。
//
// 失败服务器用 400 业务错误（isTransientBillingErr=false）规避 upstream 包内的
// 2s/4s 瞬时重试间隔——那属于 upstream 包测试域，本文件测 scheduler 侧状态机。
package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// countServer 计数的余额服务器：fail=true 恒 400（业务错误），仍每次真实打上游。
type countServer struct {
	hits   atomic.Int32
	status int
}

func (c *countServer) start(t *testing.T) (srv *httptest.Server, client *upstream.Client) {
	t.Helper()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && (r.URL.Path == "/v2/billing/meter/get-user-resource" || r.URL.Path == "/billing/meter/get-user-resource") {
			c.hits.Add(1)
		}
		if c.status == http.StatusBadRequest {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("boom"))
			return
		}
		w.Write([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CycleCapacitySize":1000,"CycleCapacityRemain":555,"CycleCapacityUsed":0}]}}}}`))
	}))
	return srv, &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
}

// seedAccount 一号一池：余额刷新只有 u1 参与。
func seedAccount(t *testing.T) (p *pool.Pool) {
	t.Helper()
	p = pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	return p
}

// TestBalanceStaggeredBackoffSkipsAfterThirdFail：三次连败计入 skipUntil，随后
// 退避窗内 staggered 轮次对上游零调用；手动路径（interval=0）不受退避干预。
func TestBalanceStaggeredBackoffSkipsAfterThirdFail(t *testing.T) {
	cs := &countServer{status: http.StatusBadRequest}
	_, up := cs.start(t)

	p := seedAccount(t)
	s := New(Config{Pool: p, Upstream: up})

	// 三轮：每轮打上游一次（连败 1、2、3）。第三轮（第三次连败）写入 skipUntil
	// = now+(3-2)×2s/2 = now+1s。
	for i := 1; i <= 3; i++ {
		n0 := cs.hits.Load()
		s.RunBalanceRefreshStaggered(context.Background(), 2*time.Second)
		if got := cs.hits.Load(); got != n0+1 {
			t.Fatalf("round %d: 上游调用 %d→%d，连败 <3 不应跳过", i, n0, got)
		}
		st := s.balanceState["u1"]
		if st.consecutive != i {
			t.Fatalf("round %d: 连败计数=%d want %d", i, st.consecutive, i)
		}
	}
	// 第三轮：连败 3 → skipUntil = now+(3-2)×2s/2 = now+1s；同轮次立即连跑应跳过。
	st := s.balanceState["u1"]
	if st.consecutive != 3 || st.skipUntil.IsZero() {
		t.Fatalf("第三轮后状态 = %+v", st)
	}
	n0 := cs.hits.Load()
	s.RunBalanceRefreshStaggered(context.Background(), 2*time.Second)
	if got := cs.hits.Load(); got != n0 {
		t.Fatalf("退避窗内上游调用 %d→%d（应零调用）", n0, got)
	}
}

// TestBalanceStaggeredBackoffEasesOffAndRecovers：退避窗滑出后恢复调用；成功一次
// 清零连败/跳过状态，credits 正常回写。
func TestBalanceStaggeredBackoffEasesOffAndRecovers(t *testing.T) {
	csFail := &countServer{status: http.StatusBadRequest}
	_, upFail := csFail.start(t)
	p := seedAccount(t)
	s := New(Config{Pool: p, Upstream: upFail})

	for i := 0; i < 3; i++ {
		s.refreshOneBalance(p.AuthByUID("u1"), "u1", 4*time.Second) // k=3 → 跳 2s
	}
	time.Sleep(2100 * time.Millisecond) // 退避窗滑出

	csOk := &countServer{}
	okSrv, upOk := csOk.start(t)
	defer okSrv.Close()
	s.mu.Lock()
	s.cfg.Upstream = upOk
	s.mu.Unlock()

	s.RunBalanceRefreshStaggered(context.Background(), 4*time.Second)
	if got := csOk.hits.Load(); got != 1 {
		t.Fatalf("退避到期后应恢复刷新，calls=%d", got)
	}
	if st := s.balanceState["u1"]; st.consecutive != 0 || !st.skipUntil.IsZero() {
		t.Fatalf("成功后状态应清零，state=%+v", st)
	}
	st2, _ := p.Status("u1")
	if st2.Credits != 555 {
		t.Fatalf("credits=%d want 555", st2.Credits)
	}
}

// TestBalanceRefreshNowNeverSkips：手动全量刷新（interval=0）只记连败、永不拦截。
func TestBalanceRefreshNowNeverSkips(t *testing.T) {
	cs := &countServer{status: http.StatusBadRequest}
	_, up := cs.start(t)
	p := seedAccount(t)
	s := New(Config{Pool: p, Upstream: up})

	for i := 1; i <= 5; i++ {
		n0 := cs.hits.Load()
		s.RunBalanceRefreshNow()
		if got, want := cs.hits.Load(), n0+1; got != want {
			t.Fatalf("iter %d: 手动刷新被拦截（calls %d→%d）", i, n0, got)
		}
		if got := s.balanceState["u1"].consecutive; got != i {
			t.Fatalf("iter %d: 连败计数=%d want %d", i, got, i)
		}
	}
	s.mu.Lock()
	active := s.balanceState["u1"].skipUntil.After(time.Now())
	s.mu.Unlock()
	if active {
		t.Fatalf("手动路径 interval=0 只计数，不应产生退避窗（skipUntil 须为零）")
	}
}

// TestBalanceStaggerSpreadContract：spread/offset 的纯函数契约。
func TestBalanceStaggerSpreadContract(t *testing.T) {
	if got := balanceStaggerSpread(0); got != 0 {
		t.Fatalf("interval=0 → spread=0, got %v", got)
	}
	if got := balanceStaggerSpread(5 * time.Minute); got != 90*time.Second {
		t.Fatalf("5m 周期摊开应封顶 90s，got %v", got)
	}
	if a, b := balanceOffset("uid-x", 90*time.Second), balanceOffset("uid-x", 90*time.Second); a != b {
		t.Fatalf("同 uid 偏移必须稳定：%v vs %v", a, b)
	}
	if got := balanceOffset("uid-x", 90*time.Second); got < 0 || got >= 90*time.Second {
		t.Fatalf("偏移超界：%v", got)
	}
	if got := balanceOffset("any", 0); got != 0 {
		t.Fatalf("spread=0 → offset=0, got %v", got)
	}
}
