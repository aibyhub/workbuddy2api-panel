// expiring_weight_test.go issue #101 权重平滑的数值断言：
// 权重因子 = 1 + 2×快过期占比 ∈ [1,3]；粘性槽位量化 1-3 档；关闭开关恒 1。
package pool

import (
	"testing"
	"time"
)

func expiringTestEntry(credits, expiring int64) *entry {
	return &entry{
		credits:                  credits,
		creditsExpiring:          expiring,
		creditsEarliestRemaining: expiring,
		creditsEarliestExpiry:    time.Now().Add(24 * time.Hour),
	}
}

func TestExpiringWeightFactor(t *testing.T) {
	cases := []struct {
		credits, expiring int64
		want              float64
	}{
		{100, 0, 1},   // 无快过期批次 → 不放大（expiringNow=false 走不到，防御口径）
		{100, 50, 2},  // 占比 0.5 → 1+1
		{100, 100, 3}, // 全部快过期 → 上限 3（与旧布尔 ×3 上限一致）
		{100, 10, 1.2},
		{0, 10, 1},    // credits<=0 防御
		{100, 200, 3}, // 占比 >1 钳 1（脏数据防御）
	}
	for _, c := range cases {
		e := expiringTestEntry(c.credits, c.expiring)
		if c.expiring == 0 {
			e.creditsEarliestRemaining = 0
			e.creditsEarliestExpiry = time.Time{}
		}
		got := expiringWeightFactor(e)
		if got != c.want {
			t.Errorf("expiringWeightFactor(credits=%d, expiring=%d) = %v, want %v", c.credits, c.expiring, got, c.want)
		}
	}
}

func TestExpiringSlotsOf(t *testing.T) {
	cases := []struct {
		credits, expiring int64
		want              int
	}{
		{100, 100, 3}, // 占比 1 → 满槽
		{100, 50, 2},  // 占比 0.5 → 2 槽（round(1.0)）
		{100, 10, 1},  // 占比 0.1 → round(1.2)=1
		{100, 30, 2},  // 占比 0.3 → round(1.6)=2
		{0, 10, 1},    // 防御
	}
	for _, c := range cases {
		got := expiringSlotsOf(expiringTestEntry(c.credits, c.expiring))
		if got != c.want {
			t.Errorf("expiringSlotsOf(credits=%d, expiring=%d) = %d, want %d", c.credits, c.expiring, got, c.want)
		}
	}
}

func TestRoutingWeightOfHonorsPreferExpiring(t *testing.T) {
	p := New("")
	p.SetPreferExpiring(false) // New 缺省 true；先显式关掉再钉「关闭即不放大」
	e := expiringTestEntry(100, 100)
	now := time.Now()
	base := p.weightOf(e, 100, now)
	// 开关关闭：不放大。
	if got := p.routingWeightOf(e, 100, now); got != base {
		t.Fatalf("prefer_expiring=false must not amplify: got %v base %v", got, base)
	}
	// 开关开启 + 全额快过期：×3。
	p.SetPreferExpiring(true)
	if got := p.routingWeightOf(e, 100, now); got != base*3 {
		t.Fatalf("full expiring ratio must be 3x: got %v want %v", got, base*3)
	}
	// 半额快过期：×2。
	e.creditsExpiring = 50
	if got := p.routingWeightOf(e, 100, now); got != base*2 {
		t.Fatalf("half expiring ratio must be 2x: got %v want %v", got, base*2)
	}
}
