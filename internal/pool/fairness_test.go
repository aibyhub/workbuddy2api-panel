// 用量均衡（load fairness）测试：窗口内被选次数的乘法降权 + 过期清理 + 容量封顶，
// 以及确定性随机源下「连续选号轮流摊开」的端到端行为。
package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestFairnessPenaltyAndWindow 降权因子随窗口内选中次数单调下降；窗口外时间戳被
// 惰性清理（权重恢复）；容量封顶后环不再膨胀。
func TestFairnessPenaltyAndWindow(t *testing.T) {
	p := New("")
	now := time.Now()
	p.Add(&auth.Auth{UID: "u1"})
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.byUID["u1"]

	if got := loadFairnessLocked(e, now); got != 1 {
		t.Fatalf("从未被选中应恒 ×1，got %v", got)
	}
	for i := 0; i < 10; i++ {
		notePickLocked(e, now)
	}
	if got, want := loadFairnessLocked(e, now), 1.0/(1.0+fairnessDecay*10); got != want {
		t.Fatalf("窗口内 10 次选中因子 = %v, want %v", got, want)
	}
	// 全部时间戳滑出窗口 → 清理后权重恢复，环清空
	if got := loadFairnessLocked(e, now.Add(fairnessWindow+time.Second)); got != 1 {
		t.Fatalf("窗口全部过期后应恢复 ×1，got %v", got)
	}
	if n := len(e.pickStamps); n != 0 {
		t.Fatalf("过期时间戳应被清理干净，got %d", n)
	}
	// 容量封顶：超发后环不膨胀，等价于恰好 fairnessCap 次
	for i := 0; i < fairnessCap+10; i++ {
		notePickLocked(e, now)
	}
	if n := len(e.pickStamps); n != fairnessCap {
		t.Fatalf("环应封顶 %d，got %d", fairnessCap, n)
	}
}

// TestFairnessSpreadsSequentialPicks 确定性随机源（r=0）下连续选号：负载惩罚让
// 选号在等权重账号间轮流摊开，24 次选号在三号内的分布差 ≤1。防撞号窗口置 0，
// 只让均衡层主导分布（100ms 真实语义下连选本来就会被 minPickGap 打散）。
func TestFairnessSpreadsSequentialPicks(t *testing.T) {
	withNoPickGap(t)

	p := New("")
	p.SetRandomSource(func(n int64) int64 { return 0 })
	for _, u := range []string{"acct-a", "acct-b", "acct-c"} {
		p.Add(&auth.Auth{UID: u})
		p.SetCredits(u, 100, 0) // 等余额：credits 项持平，惩罚项成为唯一区分度
	}

	const n = 24
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		a := p.PickExcluding(nil)
		if a == nil {
			t.Fatalf("第 %d 次选号返回 nil", i)
		}
		counts[a.UID]++
	}
	lo, hi := n, 0
	for _, c := range counts {
		if c < lo {
			lo = c
		}
		if c > hi {
			hi = c
		}
	}
	if hi-lo > 1 {
		t.Fatalf("均衡层应把 24 次选号摊到三号内（差 ≤1），counts=%v", counts)
	}
}
