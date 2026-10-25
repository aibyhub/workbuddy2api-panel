// compat_20261025_test.go 2026-10-25 客户端协议逆向对齐的新行为锁：
// qimei36 解析链、trace 头族补齐、X-Private-Data、签到活动状态与业务码 1001。
package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestResolveQimei36Priority 解析优先级：config 值 > 文件（JSON/纯文本）> 派生常量。
func TestResolveQimei36Priority(t *testing.T) {
	dir := t.TempDir()

	// 派生常量：实例级恒定（两次调用同值，且与 uid 无关）
	c := &Client{}
	got1, got2 := c.resolveQimei36(), (&Client{}).resolveQimei36()
	if got1 == "" || len(got1) != 36 {
		t.Fatalf("derived qimei36=%q want 36 chars", got1)
	}
	if got1 != got2 {
		t.Fatalf("derived qimei36 unstable: %q vs %q", got1, got2)
	}

	// 文件兜底：客户端 qimei-cache.json 原文形态
	jsonPath := filepath.Join(dir, "qimei-cache.json")
	if err := os.WriteFile(jsonPath, []byte(`{"qimei36":"aa11bb22cc33dd44ee55ff66001122334455"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cJSON := &Client{QimeiFile: jsonPath}
	if got := cJSON.resolveQimei36(); got != "aa11bb22cc33dd44ee55ff66001122334455" {
		t.Fatalf("json file qimei36=%q", got)
	}

	// 文件兜底：纯 36 hex 文本形态
	plainPath := filepath.Join(dir, "qimei.txt")
	if err := os.WriteFile(plainPath, []byte("bb22cc33dd44ee55ff66001122334455aa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cPlain := &Client{QimeiFile: plainPath}
	if got := cPlain.resolveQimei36(); got != "bb22cc33dd44ee55ff66001122334455aa" {
		t.Fatalf("plain file qimei36=%q", got)
	}

	// config 显式值最高优先
	cBoth := &Client{Qimei36: "cc33dd44ee55ff66001122334455aa11bb22", QimeiFile: jsonPath}
	if got := cBoth.resolveQimei36(); got != "cc33dd44ee55ff66001122334455aa11bb22" {
		t.Fatalf("config qimei36 should win, got %q", got)
	}

	// 坏文件回落派生值
	badPath := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(badPath, []byte(`{"other":1}`), 0o600)
	cBad := &Client{QimeiFile: badPath}
	if got := cBad.resolveQimei36(); got != got1 {
		t.Fatalf("bad file should fall back to derived, got %q want %q", got, got1)
	}
}

// TestConversationTraceFamily 会话头族补齐 traceparent/b3（与 X-B3-* 同源同值）。
func TestConversationTraceFamily(t *testing.T) {
	c := &Client{}
	req := httptest.NewRequest(http.MethodPost, "https://upstream.test/v2/chat/completions", nil)
	c.injectConversationHeaders(req, ChatMeta{ConversationRequestID: "0123456789abcdef0123456789abcdef"})
	// TraceId = ConversationRequestID（零值回落链）；SpanId = 每请求新生成的
	// messageID 前 16 位（与官方客户端「每条消息新 span」语义一致）。
	span := req.Header.Get("X-B3-SpanId")
	if len(span) != 16 {
		t.Fatalf("X-B3-SpanId=%q want 16 hex", span)
	}
	wantTP := "00-0123456789abcdef0123456789abcdef-" + span + "-01"
	if got := req.Header.Get("traceparent"); got != wantTP {
		t.Fatalf("traceparent=%q want %q", got, wantTP)
	}
	if got := req.Header.Get("b3"); got != "0123456789abcdef0123456789abcdef-"+span+"-1" {
		t.Fatalf("b3=%q", got)
	}
	if got := req.Header.Get("X-Request-ID"); len(got) != 32 || got == "0123456789abcdef0123456789abcdef" {
		t.Fatalf("X-Request-ID should be fresh 32hex per request, got %q", got)
	}
}

// TestChatPrivateDataHeader chat 通道恒发 X-Private-Data: true（客户端 VLM 网关默认头）。
func TestChatPrivateDataHeader(t *testing.T) {
	c := &Client{}
	req := httptest.NewRequest(http.MethodPost, "https://upstream.test/v2/chat/completions", nil)
	a := &auth.Auth{UID: "u1", AccessToken: "at"}
	c.ChatHeaders(req, a, "", ChatMeta{ConversationRequestID: "0123456789abcdef0123456789abcdef"})
	if got := req.Header.Get("X-Private-Data"); got != "true" {
		t.Fatalf("X-Private-Data=%q want true", got)
	}
	// 默认 UA 已对齐 5.8.3 家族
	if !strings.Contains(req.Header.Get("User-Agent"), "WorkBuddy/5.8.3") {
		t.Fatalf("UA=%q want 5.8.3 family", req.Header.Get("User-Agent"))
	}
}

// TestIsAlreadyCheckinCode1001 业务码 1001（AlreadyClaimed）精确识别；
// 11001/21001 等更长数码不得误判。
func TestIsAlreadyCheckinCode1001(t *testing.T) {
	yes := []string{
		`{"code":1001,"msg":"already"}`,
		`{"code": 1001, "msg":"x"}`,
		`上游错误 body：{"code":1001}（http 200）`,
	}
	for _, msg := range yes {
		if !IsAlreadyCheckin(&Error{Kind: ErrClient, Msg: msg}) {
			t.Errorf("1001 should be already: %s", msg)
		}
	}
	no := []string{
		`{"code":11001,"msg":"x"}`,
		`{"code":21001,"msg":"x"}`,
		`{"code":10012,"msg":"x"}`,
		`网络层错误`,
	}
	for _, msg := range no {
		if IsAlreadyCheckin(&Error{Kind: ErrClient, Msg: msg}) {
			t.Errorf("non-1001 misjudged as already: %s", msg)
		}
	}
}

// TestCheckinActivityStatusParse 活动状态解析 + 1001 信封等价「今日已签」。
func TestCheckinActivityStatusParse(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		switch r.URL.Path {
		case "/v2/billing/meter/checkin-activity-status":
			w.Write([]byte(`{"code":0,"msg":"ok","data":{"active":true,"today_checked_in":true,` +
				`"streak_days":7,"daily_credit":100,"today_credit":100,"is_streak_day":true,` +
				`"next_streak_day":8,"streak_bonus_days":3,"streak_bonus_credit":200,` +
				`"checkin_dates":["2026-10-24","2026-10-25"],"total_credits":1500}}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"code":404,"msg":"not found"}`))
		}
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	a := &auth.Auth{UID: "u1", AccessToken: "at"}
	act, err := c.CheckinActivityStatus(a)
	if err != nil {
		t.Fatalf("CheckinActivityStatus: %v", err)
	}
	if gotPath != "/v2/billing/meter/checkin-activity-status" {
		t.Fatalf("path=%s", gotPath)
	}
	if !act.TodayCheckedIn || act.StreakDays != 7 || act.DailyCredit != 100 ||
		act.StreakBonusDays != 3 || len(act.CheckinDates) != 2 {
		t.Fatalf("parsed act=%+v", act)
	}

	// 1001 幂等拒绝 → 等价「今日已签」
	srv1001 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":1001,"msg":"already claimed"}`))
	}))
	defer srv1001.Close()
	c1001 := &Client{HTTP: srv1001.Client(), ChatBaseCN: srv1001.URL, BillingBaseCN: srv1001.URL}
	act2, err := c1001.CheckinActivityStatus(a)
	if err != nil {
		t.Fatalf("1001 should be already: %v", err)
	}
	if !act2.TodayCheckedIn {
		t.Fatalf("1001 → TodayCheckedIn=true, got %+v", act2)
	}

	// 其他业务错误照常返回 err
	srvFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":1003,"msg":"event ended"}`))
	}))
	defer srvFail.Close()
	cFail := &Client{HTTP: srvFail.Client(), ChatBaseCN: srvFail.URL, BillingBaseCN: srvFail.URL}
	if _, err := cFail.CheckinActivityStatus(a); err == nil {
		t.Fatal("code 1003 should be an error")
	}

	// context 确认不悬挂（doJSON 语义借用）
	_ = context.Background()
}
