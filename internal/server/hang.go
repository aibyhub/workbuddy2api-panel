package server

// hang.go 模型级挂死熔断（fast-fail，2026-10-22 事故复盘）：
//
// 同一模型窗内 N 笔独立请求都「上游停滞」（ResponseHeaderTimeout 超时 / 传输层
// 故障 / 流内读中断）→ 熔断保持期内该模型请求入口立即 503，不再陪上游黑溺干等
// （账号轮换对"上游管道冻结"无解：同一份请求打到同一个病态后端）。成功一笔即
// 清零观察窗；换模型立即恢复。与 trae2api-web 1.5.9 同款语义（双网关口径一致）。

import (
	"log"
	"time"
)

const (
	hangWindow    = 10 * time.Minute // 出口故障计窗
	hangThreshold = 2                // 窗内两笔**独立请求**都挂死 → 熔断（单次抖动不熔断，下一笔成功即清零）
	hangHold      = 5 * time.Minute  // 熔断保持时长（到期自动放行试恢复）
)

// noteModelHang 该模型发生一笔上游停滞（每请求至多记一笔）。窗内达到阈值 →
// 熔断保持期开启（后续该模型请求入口快速失败，不再陪上游冻结干等）。
func (h *Handler) noteModelHang(model string) {
	h.hangMu.Lock()
	defer h.hangMu.Unlock()
	now := time.Now()
	hits := h.hangHits[model][:0]
	for _, t := range h.hangHits[model] {
		if now.Sub(t) < hangWindow {
			hits = append(hits, t)
		}
	}
	hits = append(hits, now)
	if len(hits) >= hangThreshold {
		h.hangUntil[model] = now.Add(hangHold)
		if len(hits) == hangThreshold { // 只在跨阈值瞬间打一行，保持期内不刷屏
			log.Printf("WARN: [server] hang guard: %s engaged for %v (%d upstream stalls within %v)",
				model, hangHold, len(hits), hangWindow)
		}
	}
	h.hangHits[model] = hits
}

// noteModelOK 模型成功一笔 → 清观察窗与熔断（恢复信号）。
func (h *Handler) noteModelOK(model string) {
	h.hangMu.Lock()
	defer h.hangMu.Unlock()
	delete(h.hangHits, model)
	delete(h.hangUntil, model)
}

// hangBlocked 该模型是否处于挂死熔断保持期。
func (h *Handler) hangBlocked(model string) bool {
	h.hangMu.Lock()
	defer h.hangMu.Unlock()
	until, ok := h.hangUntil[model]
	return ok && !until.IsZero() && time.Now().Before(until)
}
