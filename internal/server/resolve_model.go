package server

import "strings"

// resolveModelDetail 解析模型名协议（PLAN D6），并区分「realm 是否由调用方显式写出」：
//
//	分布式前缀： "[realm:]model"
//
// 取第一个 ":"，前段恰为 "cn"/"global" 才剥离；否则视为裸名，realm=cn、bare=原串。
// 大小写敏感（前缀必须是精确的小写枚举）。bare 即出站/选号/账本使用的裸模型名。
//
// 第三个返回值 realmExplicit 供跨域回退（issue #135）判定：只有裸名（false）才允许
// 在首选域选不到号时改用另一域；显式 cn:/global: 前缀永不被回退改写。
func resolveModelDetail(model string) (realm, bare string, realmExplicit bool) {
	idx := strings.IndexByte(model, ':')
	if idx < 0 {
		return "cn", model, false
	}
	prefix := model[:idx]
	if prefix != "cn" && prefix != "global" {
		return "cn", model, false
	}
	return prefix, model[idx+1:], true
}

// resolveModel 是 resolveModelDetail 的两返回值形态（既有调用方语义零改动）。
func resolveModel(model string) (realm, bare string) {
	realm, bare, _ = resolveModelDetail(model)
	return realm, bare
}

// ResolveModel 是 resolveModel 的导出面（跨包调用）。
func ResolveModel(model string) (realm, bare string) { return resolveModel(model) }

// realmChain 计算本次请求允许的域序列（跨域兜底协议，2026-10-21）：
//
//   - 裸名（无 cn:/global: 前缀）：[primary, fallback]，primary 为历史默认域（cn），
//     fallback 为跨域兜底目标（global）。裸名的域归属是网关内部约定（历史上恒 cn），
//     不是调用方的显式意图——所以允许「主域选不出号时降级到 fallback 域承接」。
//     这正是 2026-10-21 生产事故（cn 池整池 6004 限额把裸名 glm-5.3 钉死成
//     no_healthy_account，而 global 区同名模型健康）的修复：跨域兜底承接，
//     cn 配额恢复后自然回切主域。
//   - 显式前缀（cn:xxx / global:xxx）：调用方明确点名域，严格单域，不跨。
//   - fallback 为空 / 等于 primary：恒单域（fallback 机制关闭）。
func realmChain(model, primary, fallback string) []string {
	chain := []string{primary}
	if fallback == "" || fallback == primary {
		return chain
	}
	if idx := strings.IndexByte(model, ':'); idx > 0 {
		if p := model[:idx]; p == "cn" || p == "global" {
			return chain // 显式域前缀：严格单域
		}
	}
	return append(chain, fallback)
}

// realmInChain 报告 r 是否在域序列内（空串域视作不在链内）。
func realmInChain(r string, chain []string) bool {
	if r == "" {
		return false
	}
	for _, c := range chain {
		if c == r {
			return true
		}
	}
	return false
}

// otherRealm 返回另一个选号域（"cn" ↔ "global"），供跨域回退使用。
func otherRealm(realm string) string {
	if realm == "global" {
		return "cn"
	}
	return "global"
}
